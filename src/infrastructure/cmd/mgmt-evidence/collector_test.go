package main

import (
	"encoding/json"
	"testing"
)

func TestParseObjectsAcceptsListAndSingleObject(t *testing.T) {
	list, err := parseObjects([]byte(`{"items":[{"kind":"Node","metadata":{"name":"n1"}},{"kind":"Node","metadata":{"name":"n2"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list length = %d, want 2", len(list))
	}

	single, err := parseObjects([]byte(`{"kind":"OpenBao","metadata":{"name":"guardian"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 1 || kindOf(single[0]) != "OpenBao" || nameOf(single[0]) != "guardian" {
		t.Fatalf("single object parse = %#v", single)
	}
}

func TestValidateCompanySiteRequiresReadyDeploymentAndIngressHost(t *testing.T) {
	objects := []object{
		mustObject(t, `{
		  "kind":"Deployment",
		  "metadata":{"name":"company-site","namespace":"tenant-gamma"},
		  "spec":{"replicas":3},
		  "status":{"readyReplicas":3,"availableReplicas":3}
		}`),
		mustObject(t, `{"kind":"Service","metadata":{"name":"company-site","namespace":"tenant-gamma"}}`),
		mustObject(t, `{
		  "kind":"Ingress",
		  "metadata":{"name":"company-site","namespace":"tenant-gamma"},
		  "spec":{"rules":[{"host":"gamma.gi.org"}]}
		}`),
	}

	checks := validateCompanySite("gamma", "tenant-gamma", "gamma.gi.org")(objects)
	for _, c := range checks {
		if c.Status != statusPass {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

func TestValidateStorageClassesRejectsWrongDefault(t *testing.T) {
	checks := validateStorageClasses([]object{
		mustObject(t, `{"kind":"StorageClass","metadata":{"name":"local","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}`),
	})
	if len(checks) != 1 || checks[0].Status != statusFail {
		t.Fatalf("storage check = %#v, want one failing check", checks)
	}
}

func TestValidateBackupClassesRequiresPostgresAndClickHouseMappings(t *testing.T) {
	checks := validateBackupClasses([]object{
		mustObject(t, `{
		  "kind":"BackupClass",
		  "metadata":{"name":"postgres-cnpg"},
		  "spec":{"strategies":[{"application":{"kind":"Postgres"},"strategyRef":{"kind":"CNPG","name":"cnpg-default"}}]}
		}`),
		mustObject(t, `{
		  "kind":"BackupClass",
		  "metadata":{"name":"clickhouse-altinity"},
		  "spec":{"strategies":[{"application":{"kind":"ClickHouse"},"strategyRef":{"kind":"Altinity","name":"altinity-default"}}]}
		}`),
	})
	for _, c := range checks {
		if c.Status != statusPass {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

func TestValidateBackupPlansRequiresEveryTarget(t *testing.T) {
	checks := validateBackupPlans([]object{
		backupPlan(t, "tenant-root", "Postgres", "0 */6 * * *"),
		backupPlan(t, "tenant-root", "ClickHouse", "15 */6 * * *"),
		backupPlan(t, "tenant-dev", "Postgres", "0 */6 * * *"),
		backupPlan(t, "tenant-dev", "ClickHouse", "15 */6 * * *"),
		backupPlan(t, "tenant-gamma", "Postgres", "0 */6 * * *"),
		backupPlan(t, "tenant-gamma", "ClickHouse", "15 */6 * * *"),
		backupPlan(t, "tenant-prod", "Postgres", "0 */6 * * *"),
	})

	var missingClickHouseProd *check
	for i := range checks {
		if checks[i].Name == "backup.plans.prod.clickhouse.exists" {
			missingClickHouseProd = &checks[i]
		}
	}
	if missingClickHouseProd == nil || missingClickHouseProd.Status != statusFail {
		t.Fatalf("prod ClickHouse plan check = %#v, want missing failure", missingClickHouseProd)
	}
}

func TestValidateBackupRestoresAttributesInPlaceRestoreThroughBackup(t *testing.T) {
	objects := []object{
		mustObject(t, `{
		  "kind":"Backup",
		  "metadata":{"name":"pg-backup","namespace":"tenant-root"},
		  "spec":{"applicationRef":{"kind":"Postgres","name":"guardian"}}
		}`),
		mustObject(t, `{
		  "kind":"RestoreJob",
		  "metadata":{"name":"pg-restore","namespace":"tenant-root"},
		  "spec":{"backupRef":{"name":"pg-backup"}},
		  "status":{"phase":"Succeeded"}
		}`),
	}

	checks := validateBackupRestores(objects)
	var artifact, restore *check
	for i := range checks {
		switch checks[i].Name {
		case "backup.artifacts.root.postgres.exists":
			artifact = &checks[i]
		case "backup.restores.root.postgres.succeeded":
			restore = &checks[i]
		}
	}
	if artifact == nil || artifact.Status != statusPass {
		t.Fatalf("artifact check = %#v, want pass", artifact)
	}
	if restore == nil || restore.Status != statusPass {
		t.Fatalf("restore check = %#v, want pass", restore)
	}
}

func TestValidateBackupSystemRequiresSecretProjectionAndVeleroPackages(t *testing.T) {
	checks := validateBackupSystem([]object{
		readyPackage(t, "cozystack.backup-controller"),
		readyPackage(t, "cozystack.backupstrategy-controller"),
		readyPackage(t, "cozystack.external-secrets-operator"),
		readyPackage(t, "cozystack.velero"),
	})
	for _, c := range checks {
		if c.Status != statusPass {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}
}

func mustObject(t *testing.T, raw string) object {
	t.Helper()
	var obj object
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func readyPackage(t *testing.T, name string) object {
	t.Helper()
	return mustObject(t, `{
	  "kind":"Package",
	  "metadata":{"name":"`+name+`"},
	  "status":{"conditions":[{"type":"Ready","status":"True"}]}
	}`)
}

func backupPlan(t *testing.T, namespace, kind, cron string) object {
	t.Helper()
	return mustObject(t, `{
	  "kind":"Plan",
	  "metadata":{"name":"guardian-`+kind+`","namespace":"`+namespace+`"},
	  "spec":{
	    "applicationRef":{"kind":"`+kind+`","name":"guardian"},
	    "backupClassName":"`+kind+`-default",
	    "schedule":{"type":"cron","cron":"`+cron+`"}
	  }
	}`)
}
