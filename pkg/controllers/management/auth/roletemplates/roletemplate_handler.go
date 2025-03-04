package roletemplates

import (
	v3 "github.com/rancher/rancher/pkg/apis/management.cattle.io/v3"
	"github.com/rancher/rancher/pkg/rbac"
	"github.com/rancher/rancher/pkg/wrangler"
	crbacv1 "github.com/rancher/wrangler/v3/pkg/generated/controllers/rbac/v1"
	"github.com/rancher/wrangler/v3/pkg/slice"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	clusterManagementPlaneResources = map[string]string{
		"clusterscans":                "management.cattle.io",
		"clusterregistrationtokens":   "management.cattle.io",
		"clusterroletemplatebindings": "management.cattle.io",
		"etcdbackups":                 "management.cattle.io",
		"nodes":                       "management.cattle.io",
		"nodepools":                   "management.cattle.io",
		"projects":                    "management.cattle.io",
		"etcdsnapshots":               "rke.cattle.io",
	}
	projectManagementPlaneResources = map[string]string{
		"apps":                        "project.cattle.io",
		"sourcecodeproviderconfigs":   "project.cattle.io",
		"projectroletemplatebindings": "management.cattle.io",
		"secrets":                     "",
	}
)

type roleTemplateHandler struct {
	crClient crbacv1.ClusterRoleController
}

func newRoleTemplateHandler(w *wrangler.Context) *roleTemplateHandler {
	return &roleTemplateHandler{
		crClient: w.RBAC.ClusterRole(),
	}
}

// OnChange creates all management plane cluster roles that will be needed. If there are no management plane rules in the role template, no cluster roles will be created.
func (r *roleTemplateHandler) OnChange(_ string, rt *v3.RoleTemplate) (*v3.RoleTemplate, error) {
	if rt == nil || rt.DeletionTimestamp != nil {
		return nil, nil
	}

	rules, err := r.gatherRules(rt)
	if err != nil {
		return nil, err
	}

	var clusterScopedPrivileges, projectScopedPrivileges []rbacv1.PolicyRule
	if rt.Context == "project" {
		projectScopedPrivileges = getManagementPlaneRules(rules, projectManagementPlaneResources)
	} else if rt.Context == "cluster" {
		clusterScopedPrivileges = getManagementPlaneRules(rules, clusterManagementPlaneResources)
		projectScopedPrivileges = getManagementPlaneRules(rules, projectManagementPlaneResources)
	}

	var clusterRoles []*rbacv1.ClusterRole
	rtName := rbac.ClusterRoleNameFor(rt.Name)

	if len(clusterScopedPrivileges) != 0 {
		clusterRoles = append(clusterRoles,
			rbac.BuildClusterRole(rbac.ClusterManagementPlaneClusterRoleNameFor(rtName), rtName, clusterScopedPrivileges),
			rbac.BuildAggregatingClusterRole(rt, rbac.ClusterManagementPlaneClusterRoleNameFor))
	}

	if len(projectScopedPrivileges) != 0 {
		clusterRoles = append(clusterRoles,
			rbac.BuildClusterRole(rbac.ProjectManagementPlaneClusterRoleNameFor(rtName), rtName, projectScopedPrivileges),
			rbac.BuildAggregatingClusterRole(rt, rbac.ProjectManagementPlaneClusterRoleNameFor))
	}

	// Add an owner reference so the cluster role deletion gets handled automatically.
	ownerReferences := []metav1.OwnerReference{{
		Name:       rt.Name,
		APIVersion: rt.APIVersion,
		Kind:       rt.Kind,
		UID:        rt.UID,
	}}

	for _, cr := range clusterRoles {
		cr.OwnerReferences = ownerReferences
		if err := rbac.CreateOrUpdateResource(cr, r.crClient, rbac.AreClusterRolesSame); err != nil {
			return nil, err
		}
	}

	return rt, nil
}

// getManagementPlaneRules filters a set of rules based on the map passed in. Used to provide special resources that have cluster/project scope.
func getManagementPlaneRules(rules []rbacv1.PolicyRule, managementResources map[string]string) []rbacv1.PolicyRule {
	managementRules := []rbacv1.PolicyRule{}
	for _, rule := range rules {
		for resource, apiGroup := range managementResources {
			if ruleContainsManagementPlaneRule(resource, apiGroup, rule) {
				managementRules = append(managementRules, rbacv1.PolicyRule{
					Resources: []string{resource},
					APIGroups: []string{apiGroup},
					Verbs:     rule.Verbs,
				})
			}
		}
	}
	return managementRules
}

// ruleContainsManagementPlaneRule takes a rule and checks if it has the resource and apigroup from the management plane rules.
// If there are ResourceNames specified in the rule, it only applies to specific resources and doesn't count as a management plane rule.
func ruleContainsManagementPlaneRule(resource, apiGroup string, rule rbacv1.PolicyRule) bool {
	return len(rule.ResourceNames) == 0 &&
		(slice.ContainsString(rule.Resources, resource) || slice.ContainsString(rule.Resources, rbacv1.ResourceAll)) &&
		(slice.ContainsString(rule.APIGroups, apiGroup) || slice.ContainsString(rule.APIGroups, rbacv1.APIGroupAll))
}

// gatherRules returns the Rules used by the RoleTemplate. If external, it prioritizes the external rules. Otherwise use the RoleTemplate.Rules field.
func (r *roleTemplateHandler) gatherRules(rt *v3.RoleTemplate) ([]rbacv1.PolicyRule, error) {
	if rt.External {
		if rt.ExternalRules != nil {
			return rt.ExternalRules, nil
		}
		cr, err := r.crClient.Get(rt.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return cr.Rules, nil
	} else {
		return rt.Rules, nil
	}
}
