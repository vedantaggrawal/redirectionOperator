// pkg/utils/domain.go
package utils

import (
	"fmt"
	"sort"
	"strings"

	redirectorv1 "github.com/vedantaggrawal/redirectionOperator/api/v1"
)

// ExtractParentDomain extracts the parent domain from a given domain
// Examples:
// - example.com -> example.com
// - api.example.com -> example.com
// - www.sub.example.com -> example.com
func ExtractParentDomain(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) <= 2 {
		return domain
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// GroupSourcesByParentDomain groups sources by their parent domains
func GroupSourcesByParentDomain(sources []string) map[string][]string {
	groups := make(map[string][]string)

	for _, source := range sources {
		parent := ExtractParentDomain(source)
		groups[parent] = append(groups[parent], source)
	}

	// Sort sources within each parent domain for consistency
	for parent := range groups {
		sort.Strings(groups[parent])
	}

	return groups
}

// ComputeDifferences calculates the differences between old and new source lists
type DomainDifferences struct {
	Added   map[string][]string // parent domain -> added sources
	Removed map[string][]string // parent domain -> removed sources
	Changed []string            // list of parent domains that changed
}

func ComputeDifferences(oldSources, newSources []string) DomainDifferences {
	oldGroups := GroupSourcesByParentDomain(oldSources)
	newGroups := GroupSourcesByParentDomain(newSources)

	diff := DomainDifferences{
		Added:   make(map[string][]string),
		Removed: make(map[string][]string),
		Changed: []string{},
	}

	// Find all parent domains involved
	allParents := make(map[string]bool)
	for parent := range oldGroups {
		allParents[parent] = true
	}
	for parent := range newGroups {
		allParents[parent] = true
	}

	// Process each parent domain
	for parent := range allParents {
		oldSet := stringSliceToSet(oldGroups[parent])
		newSet := stringSliceToSet(newGroups[parent])

		added := setDifference(newSet, oldSet)
		removed := setDifference(oldSet, newSet)

		if len(added) > 0 || len(removed) > 0 {
			diff.Changed = append(diff.Changed, parent)

			if len(added) > 0 {
				diff.Added[parent] = setToStringSlice(added)
			}
			if len(removed) > 0 {
				diff.Removed[parent] = setToStringSlice(removed)
			}
		}
	}

	sort.Strings(diff.Changed)
	return diff
}

// GroupManager manages the grouping logic for sources within a parent domain
type GroupManager struct {
	MaxHosts int
	Groups   map[string]redirectorv1.GroupInfo
}

// NewGroupManager creates a new group manager
func NewGroupManager(maxHosts int, existingGroups map[string]redirectorv1.GroupInfo) *GroupManager {
	if existingGroups == nil {
		existingGroups = make(map[string]redirectorv1.GroupInfo)
	}

	return &GroupManager{
		MaxHosts: maxHosts,
		Groups:   existingGroups,
	}
}

// AddSources adds new sources to the groups, using optimal placement strategy
func (gm *GroupManager) AddSources(sources []string, parentDomain, destination string) {
	for _, source := range sources {
		gm.addSingleSource(source, parentDomain, destination)
	}
}

// RemoveSources removes sources from the groups and cleans up empty groups
func (gm *GroupManager) RemoveSources(sources []string) {
	for _, source := range sources {
		gm.removeSingleSource(source)
	}

	// Clean up empty groups
	gm.cleanupEmptyGroups()
}

// addSingleSource adds a single source to the optimal group
func (gm *GroupManager) addSingleSource(source, parentDomain, destination string) {
	// Find the group with highest occupancy that still has space
	bestGroupIndex := ""
	bestGroupSize := -1

	for groupIndex, group := range gm.Groups {
		if group.Counter < gm.MaxHosts && group.Counter > bestGroupSize {
			bestGroupIndex = groupIndex
			bestGroupSize = group.Counter
		}
	}

	// If no group has space, create a new one
	if bestGroupIndex == "" {
		bestGroupIndex = gm.getNextGroupIndex()
		gm.Groups[bestGroupIndex] = redirectorv1.GroupInfo{
			Counter:    0,
			Sources:    make(map[string]struct{}),
			SecretName: fmt.Sprintf("cert-%s-%s-%s", strings.ReplaceAll(parentDomain, ".", "-"), strings.ReplaceAll(destination, ".", "-"), bestGroupIndex),
		}
	}

	// Add the source to the selected group
	group := gm.Groups[bestGroupIndex]
	if group.Sources == nil {
		group.Sources = make(map[string]struct{})
	}
	group.Sources[source] = struct{}{}
	group.Counter = len(group.Sources)
	gm.Groups[bestGroupIndex] = group
}

// removeSingleSource removes a single source from its group
func (gm *GroupManager) removeSingleSource(source string) {
	for groupIndex, group := range gm.Groups {
		if _, exists := group.Sources[source]; exists {
			delete(group.Sources, source)
			group.Counter = len(group.Sources)
			gm.Groups[groupIndex] = group
			break
		}
	}
}

// cleanupEmptyGroups removes groups with no sources
func (gm *GroupManager) cleanupEmptyGroups() {
	for groupIndex, group := range gm.Groups {
		if group.Counter == 0 {
			delete(gm.Groups, groupIndex)
		}
	}
}

// getNextGroupIndex finds the next available group index
func (gm *GroupManager) getNextGroupIndex() string {
	maxIndex := -1
	for groupIndex := range gm.Groups {
		if idx := parseInt(groupIndex); idx > maxIndex {
			maxIndex = idx
		}
	}
	return fmt.Sprintf("%d", maxIndex+1)
}

// GetAllSources returns all sources across all groups
func (gm *GroupManager) GetAllSources() []string {
	var allSources []string
	for _, group := range gm.Groups {
		for source := range group.Sources {
			allSources = append(allSources, source)
		}
	}
	sort.Strings(allSources)
	return allSources
}

// Helper functions

func stringSliceToSet(slice []string) map[string]bool {
	set := make(map[string]bool)
	for _, item := range slice {
		set[item] = true
	}
	return set
}

func setDifference(set1, set2 map[string]bool) map[string]bool {
	diff := make(map[string]bool)
	for item := range set1 {
		if !set2[item] {
			diff[item] = true
		}
	}
	return diff
}

func setToStringSlice(set map[string]bool) []string {
	var slice []string
	for item := range set {
		slice = append(slice, item)
	}
	sort.Strings(slice)
	return slice
}

func parseInt(s string) int {
	result := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		} else {
			break
		}
	}
	return result
}

// GenerateDomainBindingName generates a consistent name for a DomainBinding
func GenerateDomainBindingName(parentDomain, destination string) string {
	// Replace dots with dashes and ensure valid k8s name
	name := fmt.Sprintf("%s-%s",
		strings.ReplaceAll(parentDomain, ".", "-"),
		strings.ReplaceAll(destination, ".", "-"))

	// Ensure the name is not too long (k8s limit is 253 characters)
	if len(name) > 240 {
		name = name[:240]
	}

	return name
}

// GenerateIngressName generates a consistent name for an Ingress
func GenerateIngressName(parentDomain, destination string) string {
	return fmt.Sprintf("ingress-%s", GenerateDomainBindingName(parentDomain, destination))
}
