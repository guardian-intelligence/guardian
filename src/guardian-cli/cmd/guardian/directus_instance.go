package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type directusInstanceManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec directusInstanceSpec `yaml:"spec"`
}

type directusInstanceSpec struct {
	Site             string            `yaml:"site"`
	Namespace        string            `yaml:"namespace"`
	NamespaceLabels  map[string]string `yaml:"namespaceLabels"`
	Image            string            `yaml:"image"`
	PostgresImage    string            `yaml:"postgresImage"`
	PublicAdminRoute bool              `yaml:"publicAdminRoute"`
	AdminDomain      string            `yaml:"adminDomain"`
	Resources        struct {
		Requests map[string]string `yaml:"requests"`
		Limits   map[string]string `yaml:"limits"`
	} `yaml:"resources"`
	Secrets struct {
		Projection struct {
			WaitForSecrets *bool `yaml:"waitForSecrets"`
			OpenBao        struct {
				Role               string `yaml:"role"`
				ServiceAccountName string `yaml:"serviceAccountName"`
			} `yaml:"openbao"`
			RemotePaths struct {
				Runtime  string `yaml:"runtime"`
				Admin    string `yaml:"admin"`
				Database string `yaml:"database"`
			} `yaml:"remotePaths"`
		} `yaml:"projection"`
		RuntimeSecretName   string `yaml:"runtimeSecretName"`
		RuntimeSecretKey    string `yaml:"runtimeSecretKey"`
		AdminSecretName     string `yaml:"adminSecretName"`
		AdminPasswordKey    string `yaml:"adminPasswordKey"`
		DatabaseSecretName  string `yaml:"databaseSecretName"`
		DatabasePasswordKey string `yaml:"databasePasswordKey"`
	} `yaml:"secrets"`
	Admin struct {
		Email string `yaml:"email"`
	} `yaml:"admin"`
	Database struct {
		Name        string          `yaml:"name"`
		User        string          `yaml:"user"`
		Persistence persistenceSpec `yaml:"persistence"`
	} `yaml:"database"`
	Uploads struct {
		Persistence persistenceSpec `yaml:"persistence"`
	} `yaml:"uploads"`
	Storage struct {
		S3 *directusS3Storage `yaml:"s3"`
	} `yaml:"storage"`
	Gateway struct {
		Name            string `yaml:"name"`
		Namespace       string `yaml:"namespace"`
		HTTPSectionName string `yaml:"httpSectionName"`
	} `yaml:"gateway"`
}

type directusS3Storage struct {
	Enabled            bool   `yaml:"enabled"`
	Bucket             string `yaml:"bucket"`
	Region             string `yaml:"region"`
	Endpoint           string `yaml:"endpoint"`
	RemotePath         string `yaml:"remotePath"`
	SecretName         string `yaml:"secretName"`
	AccessKeyIDKey     string `yaml:"accessKeyIDKey"`
	SecretAccessKeyKey string `yaml:"secretAccessKeyKey"`
}

func directusInstances(site *Site) ([]directusInstanceManifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(site.EnvironmentBundle.Raw))
	var out []directusInstanceManifest
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", site.EnvironmentBundle.Path, err)
		}
		if node.Kind == 0 {
			continue
		}
		var header struct {
			Kind string `yaml:"kind"`
		}
		if err := node.Decode(&header); err != nil {
			return nil, fmt.Errorf("decode %s document header: %w", site.EnvironmentBundle.Path, err)
		}
		if header.Kind != "DirectusInstance" {
			continue
		}
		var doc directusInstanceManifest
		if err := node.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode %s DirectusInstance: %w", site.EnvironmentBundle.Path, err)
		}
		out = append(out, doc)
	}
	if err := validateDirectusInstances(site, out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateDirectusInstances(site *Site, instances []directusInstanceManifest) error {
	for _, instance := range instances {
		if err := validateDirectusInstance(site, instance); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectusInstance(site *Site, instance directusInstanceManifest) error {
	name := instance.Metadata.Name
	spec := instance.Spec
	if name == "" {
		return fmt.Errorf("environment %s: DirectusInstance metadata.name is required", site.EnvironmentBundle.Path)
	}
	required := map[string]string{
		"site":                            spec.Site,
		"namespace":                       spec.Namespace,
		"image":                           spec.Image,
		"postgresImage":                   spec.PostgresImage,
		"secrets.projection.openbao.role": spec.Secrets.Projection.OpenBao.Role,
		"secrets.projection.openbao.serviceAccountName": spec.Secrets.Projection.OpenBao.ServiceAccountName,
		"secrets.projection.remotePaths.runtime":        spec.Secrets.Projection.RemotePaths.Runtime,
		"secrets.projection.remotePaths.admin":          spec.Secrets.Projection.RemotePaths.Admin,
		"secrets.projection.remotePaths.database":       spec.Secrets.Projection.RemotePaths.Database,
		"secrets.runtimeSecretName":                     spec.Secrets.RuntimeSecretName,
		"secrets.runtimeSecretKey":                      spec.Secrets.RuntimeSecretKey,
		"secrets.adminSecretName":                       spec.Secrets.AdminSecretName,
		"secrets.adminPasswordKey":                      spec.Secrets.AdminPasswordKey,
		"secrets.databaseSecretName":                    spec.Secrets.DatabaseSecretName,
		"secrets.databasePasswordKey":                   spec.Secrets.DatabasePasswordKey,
		"admin.email":                                   spec.Admin.Email,
		"database.name":                                 spec.Database.Name,
		"database.user":                                 spec.Database.User,
		"gateway.name":                                  spec.Gateway.Name,
		"gateway.namespace":                             spec.Gateway.Namespace,
		"gateway.httpSectionName":                       spec.Gateway.HTTPSectionName,
		"resources.requests.cpu":                        spec.Resources.Requests["cpu"],
		"resources.requests.memory":                     spec.Resources.Requests["memory"],
		"resources.limits.memory":                       spec.Resources.Limits["memory"],
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("environment %s: DirectusInstance %s %s is required", site.EnvironmentBundle.Path, name, field)
		}
	}
	if spec.Site != site.Name {
		return fmt.Errorf("environment %s: DirectusInstance %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
	}
	if spec.PublicAdminRoute {
		if spec.AdminDomain == "" {
			return fmt.Errorf("environment %s: DirectusInstance %s publicAdminRoute requires adminDomain", site.EnvironmentBundle.Path, name)
		}
		if strings.Contains(spec.AdminDomain, "://") || strings.Contains(spec.AdminDomain, "/") {
			return fmt.Errorf("environment %s: DirectusInstance %s adminDomain must be a hostname, got %q", site.EnvironmentBundle.Path, name, spec.AdminDomain)
		}
	}
	if err := validatePersistence(site, "DirectusInstance "+name+" database", spec.Namespace, spec.Database.Persistence); err != nil {
		return err
	}
	if spec.Storage.S3 == nil || !spec.Storage.S3.Enabled {
		if err := validatePersistence(site, "DirectusInstance "+name+" uploads", spec.Namespace, spec.Uploads.Persistence); err != nil {
			return err
		}
	}
	secretNames := []string{
		spec.Secrets.RuntimeSecretName,
		spec.Secrets.AdminSecretName,
		spec.Secrets.DatabaseSecretName,
	}
	if spec.Storage.S3 != nil && spec.Storage.S3.Enabled {
		s3 := spec.Storage.S3
		s3Required := map[string]string{
			"storage.s3.bucket":             s3.Bucket,
			"storage.s3.region":             s3.Region,
			"storage.s3.endpoint":           s3.Endpoint,
			"storage.s3.remotePath":         s3.RemotePath,
			"storage.s3.secretName":         s3.SecretName,
			"storage.s3.accessKeyIDKey":     s3.AccessKeyIDKey,
			"storage.s3.secretAccessKeyKey": s3.SecretAccessKeyKey,
		}
		for field, value := range s3Required {
			if value == "" {
				return fmt.Errorf("environment %s: DirectusInstance %s storage.s3.enabled requires %s", site.EnvironmentBundle.Path, name, field)
			}
		}
		secretNames = append(secretNames, s3.SecretName)
	}
	projection := directusSecretProjection(instance)
	if err := validateSecretProjection(site, projection); err != nil {
		return err
	}
	projectedSecrets := map[string]bool{}
	for _, secret := range projection.Spec.Secrets {
		projectedSecrets[secret.Name] = true
	}
	for _, secretName := range secretNames {
		if !projectedSecrets[secretName] {
			return fmt.Errorf("environment %s: DirectusInstance %s secret %s/%s is not declared by the DirectusInstance SecretProjection", site.EnvironmentBundle.Path, name, spec.Namespace, secretName)
		}
	}
	return nil
}

func directusSecretProjection(instance directusInstanceManifest) secretProjectionManifest {
	spec := instance.Spec
	waitForSecrets := false
	if spec.Secrets.Projection.WaitForSecrets != nil {
		waitForSecrets = *spec.Secrets.Projection.WaitForSecrets
	}
	createNamespace := false
	projection := secretProjectionManifest{Kind: "SecretProjection"}
	projection.Metadata.Name = instance.Metadata.Name + "-secrets"
	projection.Spec.WaitForSecrets = &waitForSecrets
	projection.Spec.Target.Namespace = spec.Namespace
	projection.Spec.Target.CreateNamespace = &createNamespace
	projection.Spec.OpenBao.Role = spec.Secrets.Projection.OpenBao.Role
	projection.Spec.OpenBao.ServiceAccountName = spec.Secrets.Projection.OpenBao.ServiceAccountName
	projection.Spec.Secrets = []secretProjectionSecret{
		{
			Name:       spec.Secrets.RuntimeSecretName,
			Type:       "Opaque",
			RemotePath: spec.Secrets.Projection.RemotePaths.Runtime,
			Data: []secretProjectionData{
				{SecretKey: spec.Secrets.RuntimeSecretKey, Property: spec.Secrets.RuntimeSecretKey},
			},
		},
		{
			Name:       spec.Secrets.AdminSecretName,
			Type:       "Opaque",
			RemotePath: spec.Secrets.Projection.RemotePaths.Admin,
			Data: []secretProjectionData{
				{SecretKey: spec.Secrets.AdminPasswordKey, Property: spec.Secrets.AdminPasswordKey},
			},
		},
		{
			Name:       spec.Secrets.DatabaseSecretName,
			Type:       "Opaque",
			RemotePath: spec.Secrets.Projection.RemotePaths.Database,
			Data: []secretProjectionData{
				{SecretKey: spec.Secrets.DatabasePasswordKey, Property: spec.Secrets.DatabasePasswordKey},
			},
		},
	}
	if s3 := spec.Storage.S3; s3 != nil && s3.Enabled {
		projection.Spec.Secrets = append(projection.Spec.Secrets, secretProjectionSecret{
			Name:       s3.SecretName,
			Type:       "Opaque",
			RemotePath: s3.RemotePath,
			Data: []secretProjectionData{
				{SecretKey: s3.AccessKeyIDKey, Property: s3.AccessKeyIDKey},
				{SecretKey: s3.SecretAccessKeyKey, Property: s3.SecretAccessKeyKey},
			},
		})
	}
	return projection
}
