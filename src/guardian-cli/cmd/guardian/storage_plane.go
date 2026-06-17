package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type storagePlaneManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec storagePlaneSpec `yaml:"spec"`
}

type storagePlaneSpec struct {
	Site              string               `yaml:"site"`
	NodeName          string               `yaml:"nodeName"`
	StorageClassName  string               `yaml:"storageClassName"`
	ReclaimPolicy     string               `yaml:"reclaimPolicy"`
	VolumeBindingMode string               `yaml:"volumeBindingMode"`
	Volumes           []storagePlaneVolume `yaml:"volumes"`
}

type storagePlaneVolume struct {
	Name                 string `yaml:"name"`
	PersistentVolumeName string `yaml:"persistentVolumeName"`
	Namespace            string `yaml:"namespace"`
	ClaimName            string `yaml:"claimName"`
	Capacity             string `yaml:"capacity"`
	LocalPath            string `yaml:"localPath"`
}

type persistenceSpec struct {
	ClaimName        string   `yaml:"claimName"`
	StorageClassName string   `yaml:"storageClassName"`
	Size             string   `yaml:"size"`
	VolumeName       string   `yaml:"volumeName"`
	AccessModes      []string `yaml:"accessModes"`
}

func storagePlanes(site *Site) ([]storagePlaneManifest, error) {
	var out []storagePlaneManifest
	if err := decodeEnvironmentDocuments(site.EnvironmentBundle.Raw, site.EnvironmentBundle.Path, "StoragePlane", func(node *yaml.Node) error {
		var doc storagePlaneManifest
		if err := node.Decode(&doc); err != nil {
			return err
		}
		out = append(out, doc)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := validateStoragePlanes(site, out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateStoragePlanes(site *Site, planes []storagePlaneManifest) error {
	if len(planes) != 1 {
		return fmt.Errorf("environment %s: exactly one StoragePlane is required, found %d", site.EnvironmentBundle.Path, len(planes))
	}
	return validateStoragePlane(site, planes[0])
}

func validateStoragePlane(site *Site, plane storagePlaneManifest) error {
	name := plane.Metadata.Name
	spec := plane.Spec
	if name == "" {
		return fmt.Errorf("environment %s: StoragePlane metadata.name is required", site.EnvironmentBundle.Path)
	}
	required := map[string]string{
		"site":              spec.Site,
		"nodeName":          spec.NodeName,
		"storageClassName":  spec.StorageClassName,
		"reclaimPolicy":     spec.ReclaimPolicy,
		"volumeBindingMode": spec.VolumeBindingMode,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("environment %s: StoragePlane %s spec.%s is required", site.EnvironmentBundle.Path, name, field)
		}
	}
	if spec.Site != site.Name {
		return fmt.Errorf("environment %s: StoragePlane %s spec.site = %q, want %q", site.EnvironmentBundle.Path, name, spec.Site, site.Name)
	}
	if spec.NodeName != site.Node.Hostname {
		return fmt.Errorf("environment %s: StoragePlane %s spec.nodeName = %q, want bootstrap node.hostname %q", site.EnvironmentBundle.Path, name, spec.NodeName, site.Node.Hostname)
	}
	if spec.ReclaimPolicy != "Retain" {
		return fmt.Errorf("environment %s: StoragePlane %s spec.reclaimPolicy must be Retain, got %q", site.EnvironmentBundle.Path, name, spec.ReclaimPolicy)
	}
	if spec.VolumeBindingMode != "WaitForFirstConsumer" {
		return fmt.Errorf("environment %s: StoragePlane %s spec.volumeBindingMode must be WaitForFirstConsumer, got %q", site.EnvironmentBundle.Path, name, spec.VolumeBindingMode)
	}
	if len(spec.Volumes) == 0 {
		return fmt.Errorf("environment %s: StoragePlane %s spec.volumes is required", site.EnvironmentBundle.Path, name)
	}
	seenNames := map[string]bool{}
	seenPVs := map[string]bool{}
	seenClaims := map[string]bool{}
	for i, volume := range spec.Volumes {
		prefix := fmt.Sprintf("StoragePlane %s spec.volumes[%d]", name, i)
		required := map[string]string{
			"name":                 volume.Name,
			"persistentVolumeName": volume.PersistentVolumeName,
			"namespace":            volume.Namespace,
			"claimName":            volume.ClaimName,
			"capacity":             volume.Capacity,
			"localPath":            volume.LocalPath,
		}
		for field, value := range required {
			if value == "" {
				return fmt.Errorf("environment %s: %s.%s is required", site.EnvironmentBundle.Path, prefix, field)
			}
		}
		if seenNames[volume.Name] {
			return fmt.Errorf("environment %s: %s.name duplicates %q", site.EnvironmentBundle.Path, prefix, volume.Name)
		}
		seenNames[volume.Name] = true
		if seenPVs[volume.PersistentVolumeName] {
			return fmt.Errorf("environment %s: %s.persistentVolumeName duplicates %q", site.EnvironmentBundle.Path, prefix, volume.PersistentVolumeName)
		}
		seenPVs[volume.PersistentVolumeName] = true
		claimKey := volume.Namespace + "/" + volume.ClaimName
		if seenClaims[claimKey] {
			return fmt.Errorf("environment %s: %s claim duplicates %s", site.EnvironmentBundle.Path, prefix, claimKey)
		}
		seenClaims[claimKey] = true
		if !filepath.IsAbs(volume.LocalPath) {
			return fmt.Errorf("environment %s: %s.localPath must be absolute, got %q", site.EnvironmentBundle.Path, prefix, volume.LocalPath)
		}
		if !pathWithin(volume.LocalPath, site.Storage.ProductPool.Mountpoint) {
			return fmt.Errorf("environment %s: %s.localPath %q must be under bootstrap storage pool mountpoint %q", site.EnvironmentBundle.Path, prefix, volume.LocalPath, site.Storage.ProductPool.Mountpoint)
		}
	}
	return nil
}

func validatePersistence(site *Site, owner, namespace string, persistence persistenceSpec) error {
	required := map[string]string{
		"persistence.claimName":        persistence.ClaimName,
		"persistence.storageClassName": persistence.StorageClassName,
		"persistence.size":             persistence.Size,
		"persistence.volumeName":       persistence.VolumeName,
	}
	for field, value := range required {
		if value == "" {
			return fmt.Errorf("environment %s: %s spec.%s is required", site.EnvironmentBundle.Path, owner, field)
		}
	}
	if len(persistence.AccessModes) == 0 {
		return fmt.Errorf("environment %s: %s spec.persistence.accessModes is required", site.EnvironmentBundle.Path, owner)
	}
	if site.StoragePlane == nil {
		return fmt.Errorf("environment %s: %s cannot validate persistence before StoragePlane is loaded", site.EnvironmentBundle.Path, owner)
	}
	for _, mode := range persistence.AccessModes {
		if mode == "" {
			return fmt.Errorf("environment %s: %s spec.persistence.accessModes cannot contain empty values", site.EnvironmentBundle.Path, owner)
		}
	}
	for _, volume := range site.StoragePlane.Spec.Volumes {
		if volume.Namespace == namespace && volume.ClaimName == persistence.ClaimName {
			if volume.PersistentVolumeName != persistence.VolumeName {
				return fmt.Errorf("environment %s: %s persistence.volumeName = %q, want StoragePlane PV %q", site.EnvironmentBundle.Path, owner, persistence.VolumeName, volume.PersistentVolumeName)
			}
			if volume.Capacity != persistence.Size {
				return fmt.Errorf("environment %s: %s persistence.size = %q, want StoragePlane capacity %q", site.EnvironmentBundle.Path, owner, persistence.Size, volume.Capacity)
			}
			if persistence.StorageClassName != site.StoragePlane.Spec.StorageClassName {
				return fmt.Errorf("environment %s: %s persistence.storageClassName = %q, want StoragePlane storageClassName %q", site.EnvironmentBundle.Path, owner, persistence.StorageClassName, site.StoragePlane.Spec.StorageClassName)
			}
			return nil
		}
	}
	return fmt.Errorf("environment %s: %s persistence claim %s/%s has no matching StoragePlane volume", site.EnvironmentBundle.Path, owner, namespace, persistence.ClaimName)
}

func pathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}
