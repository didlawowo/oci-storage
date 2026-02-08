// pkg/models/chart.go
package models

import (
	"sort"

	"github.com/Masterminds/semver/v3"
)

// ChartMetadata représente la structure commune utilisée dans toute l'application
type ChartMetadata struct {
	Name         string `yaml:"name"`
	Version      string `yaml:"version"`
	Description  string `yaml:"description"`
	ApiVersion   string `yaml:"apiVersion"`
	Type         string `yaml:"type,omitempty"`
	AppVersion   string `yaml:"appVersion,omitempty"`
	Dependencies []struct {
		Name       string `yaml:"name"`
		Version    string `yaml:"version"`
		Repository string `yaml:"repository"`
	} `yaml:"dependencies,omitempty"`
}

type ChartGroup struct {
	Name     string          // Nom du chart
	Versions []ChartMetadata // Liste des versions disponibles
}

func GroupChartsByName(charts []ChartMetadata) []ChartGroup {
	// Map pour regrouper les versions par nom
	chartGroups := make(map[string][]ChartMetadata)

	// Regrouper toutes les versions par nom
	for _, chart := range charts {
		chartGroups[chart.Name] = append(chartGroups[chart.Name], chart)
	}

	// Convertir la map en slice et trier les versions (newest first)
	result := make([]ChartGroup, 0, len(chartGroups))
	for name, versions := range chartGroups {
		sort.Slice(versions, func(i, j int) bool {
			vi, errI := semver.NewVersion(versions[i].Version)
			vj, errJ := semver.NewVersion(versions[j].Version)
			if errI != nil || errJ != nil {
				// Fallback to string comparison if not valid semver
				return versions[i].Version > versions[j].Version
			}
			return vi.GreaterThan(vj)
		})
		result = append(result, ChartGroup{
			Name:     name,
			Versions: versions,
		})
	}

	// Sort groups alphabetically by name
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}
