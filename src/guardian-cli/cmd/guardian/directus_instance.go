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
	Site             string `yaml:"site"`
	Namespace        string `yaml:"namespace"`
	Image            string `yaml:"image"`
	PostgresImage    string `yaml:"postgresImage"`
	PublicAdminRoute bool   `yaml:"publicAdminRoute"`
	AdminDomain      string `yaml:"adminDomain"`
	Resources        struct {
		Requests map[string]string `yaml:"requests"`
		Limits   map[string]string `yaml:"limits"`
	} `yaml:"resources"`
	Secrets struct {
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
		Name        string `yaml:"name"`
		User        string `yaml:"user"`
		StoragePath string `yaml:"storagePath"`
	} `yaml:"database"`
	UploadsPath string `yaml:"uploadsPath"`
	Storage     struct {
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
	projectedSecrets, err := projectedSecretsByNamespace(site)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if err := validateDirectusInstance(site, instance, projectedSecrets); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectusInstance(site *Site, instance directusInstanceManifest, projectedSecrets map[string]map[string]bool) error {
	name := instance.Metadata.Name
	spec := instance.Spec
	if name == "" {
		return fmt.Errorf("environment %s: DirectusInstance metadata.name is required", site.EnvironmentBundle.Path)
	}
	required := map[string]string{
		"site":                        spec.Site,
		"namespace":                   spec.Namespace,
		"image":                       spec.Image,
		"postgresImage":               spec.PostgresImage,
		"secrets.runtimeSecretName":   spec.Secrets.RuntimeSecretName,
		"secrets.runtimeSecretKey":    spec.Secrets.RuntimeSecretKey,
		"secrets.adminSecretName":     spec.Secrets.AdminSecretName,
		"secrets.adminPasswordKey":    spec.Secrets.AdminPasswordKey,
		"secrets.databaseSecretName":  spec.Secrets.DatabaseSecretName,
		"secrets.databasePasswordKey": spec.Secrets.DatabasePasswordKey,
		"admin.email":                 spec.Admin.Email,
		"database.name":               spec.Database.Name,
		"database.user":               spec.Database.User,
		"database.storagePath":        spec.Database.StoragePath,
		"uploadsPath":                 spec.UploadsPath,
		"gateway.name":                spec.Gateway.Name,
		"gateway.namespace":           spec.Gateway.Namespace,
		"gateway.httpSectionName":     spec.Gateway.HTTPSectionName,
		"resources.requests.cpu":      spec.Resources.Requests["cpu"],
		"resources.requests.memory":   spec.Resources.Requests["memory"],
		"resources.limits.memory":     spec.Resources.Limits["memory"],
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
	for _, secretName := range secretNames {
		if !projectedSecrets[spec.Namespace][secretName] {
			return fmt.Errorf("environment %s: DirectusInstance %s secret %s/%s is not declared by a SecretProjection", site.EnvironmentBundle.Path, name, spec.Namespace, secretName)
		}
	}
	return nil
}

func projectedSecretsByNamespace(site *Site) (map[string]map[string]bool, error) {
	projections, err := secretProjections(site)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, projection := range projections {
		namespace := projection.Spec.Target.Namespace
		if out[namespace] == nil {
			out[namespace] = map[string]bool{}
		}
		for _, secret := range projection.Spec.Secrets {
			out[namespace][secret.Name] = true
		}
	}
	return out, nil
}
