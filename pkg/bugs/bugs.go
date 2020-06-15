package bugs

import (
	"fmt"
	"io/ioutil"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eparis/bugtool/pkg/teams"
	"github.com/eparis/bugzilla"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	UpcomingSprint   = "UpcomingSprint"
	APIKeyFlagName   = "bugzilla-key"
	apiKeyFlagDefVal = "bugzillaKey"
	apiKeyFlagUsage  = "Path to file containing BZ API key"

	bugDataFlagName   = "test-bug-data"
	bugDataFlagDefVal = ""
	bugDataFlagUsage  = "Path to file containing test bug data"
)

type BugMap map[string][]*bugzilla.Bug

func (b BugMap) Teams() []string {
	out := []string{}
	for team := range b {
		out = append(out, team)
	}
	sort.Strings(out)
	return out
}

func (b BugMap) FilterByTargetRelease(sTargets []string) BugMap {
	targets := sets.NewString(sTargets...)
	out := BugMap{}

	for team, bugs := range b {
		filtered := []*bugzilla.Bug{}
		for i := range bugs {
			bug := bugs[i]
			if !targets.Has(bug.TargetRelease[0]) {
				continue
			}
			filtered = append(filtered, bug)
		}
		out[team] = filtered
	}
	return out
}

func (b BugMap) FilterBySeverity(sSeverities []string) BugMap {
	severities := sets.NewString(sSeverities...)
	out := BugMap{}

	for team, bugs := range b {
		filtered := []*bugzilla.Bug{}
		for i := range bugs {
			bug := bugs[i]
			if !severities.Has(bug.Severity) {
				continue
			}
			filtered = append(filtered, bug)
		}
		out[team] = filtered
	}
	return out
}

func (b BugMap) CountAll(team string) int {
	return len(b[team])
}

func (b BugMap) CountUpcomingSprint(team string) int {
	count := 0
	for _, bug := range b[team] {
		for _, found := range bug.Keywords {
			if found == UpcomingSprint {
				count += 1
				break
			}
		}
	}
	return count
}

func (b BugMap) CountNotUpcomingSprint(team string) int {
	return b.CountAll(team) - b.CountUpcomingSprint(team)
}

func (b BugMap) CountLowSeverity(team string) int {
	count := 0
	for _, bug := range b[team] {
		if bug.Severity == "low" {
			count += 1
		}
	}
	return count
}

func (b BugMap) CountNotLowSeverity(team string) int {
	return b.CountAll(team) - b.CountLowSeverity(team)
}

func (b BugMap) CountTargetRelease(team string, targets []string) int {
	count := 0
	for _, bug := range b[team] {
		targetRelease := bug.TargetRelease
		for _, target := range targets {
			if targetRelease[0] == target {
				count += 1
				break
			}
		}
	}
	return count
}

func (b BugMap) CountBlocker(team string, targets []string) int {
	count := 0
	for _, bug := range b[team] {
		targetRelease := bug.TargetRelease
		severity := bug.Severity

		if severity == "low" {
			continue
		}
		for _, target := range targets {
			if targetRelease[0] == target {
				count += 1
				break
			}
		}
	}
	return count
}

type BugData struct {
	sync.RWMutex
	bugs    []*bugzilla.Bug
	bugMap  BugMap
	cmd     *cobra.Command
	errs    chan error
	client  bugzilla.Client
	query   bugzilla.Query
	orgData *teams.OrgData
}

func (bd *BugData) GetBugs() []*bugzilla.Bug {
	bd.RLock()
	defer bd.RUnlock()
	return bd.bugs
}

func (bd *BugData) GetBugMap() BugMap {
	bd.RLock()
	defer bd.RUnlock()
	return bd.bugMap
}

func (bd *BugData) set(bugs []*bugzilla.Bug, bugMap map[string][]*bugzilla.Bug) {
	bd.Lock()
	defer bd.Unlock()
	bd.bugs = bugs
	bd.bugMap = BugMap(bugMap)
}

func (bd *BugData) reconcile() error {
	bugs, err := bd.client.Search(bd.query)
	if err != nil {
		return err
	}
	bugMap, err := buildTeamMap(bugs, bd.orgData)
	if err != nil {
		return err
	}
	bd.set(bugs, bugMap)
	fmt.Printf("Successfully reconciled GetBugData. Teams:%d BugCount:%d\n", len(bd.orgData.Teams), len(bd.GetBugs()))
	return nil
}

type testClient struct {
	path string
}

func (tc testClient) UpdateBug(_ int, _ bugzilla.BugUpdate) error {
	return nil
}
func (tc testClient) Search(_ bugzilla.Query) ([]*bugzilla.Bug, error) {
	return []*bugzilla.Bug{}, nil
}
func (tc testClient) GetExternalBugPRsOnBug(_ int) ([]bugzilla.ExternalBug, error) {
	return []bugzilla.ExternalBug{}, nil
}
func (tc testClient) GetBug(_ int) (*bugzilla.Bug, error) {
	return &bugzilla.Bug{}, nil
}
func (tc testClient) Endpoint() string {
	return tc.path
}
func (testClient) AddPullRequestAsExternalBug(_ int, _ string, _ string, _ int) (bool, error) {
	return false, nil
}

func BugzillaClient(cmd *cobra.Command) (bugzilla.Client, error) {
	if testPath, err := cmd.Flags().GetString(bugDataFlagName); err != nil {
		return nil, err
	} else if testPath != "" {
		return bugzilla.GetTestClient(testPath), nil
	}

	endpoint := "https://bugzilla.redhat.com"

	keyFile, err := cmd.Flags().GetString(APIKeyFlagName)
	dat, err := ioutil.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	apikey := strings.TrimRight(string(dat), "\r\n")

	var generator *func() []byte
	generatorFunc := func() []byte {
		return []byte(apikey)
	}
	generator = &generatorFunc

	return bugzilla.NewClient(*generator, endpoint), nil
}

func getAllOpenBugsQuery() bugzilla.Query {
	return bugzilla.Query{
		Classification: []string{"Red Hat"},
		Product:        []string{"OpenShift Container Platform"},
		Status:         []string{"NEW", "ASSIGNED", "POST", "ON_DEV", "MODIFIED"},
		IncludeFields:  []string{"id", "summary", "status", "severity", "target_release", "component", "sub_components", "keywords"},
		Advanced: []bugzilla.AdvancedQuery{
			{
				Field:  "component",
				Op:     "equals",
				Value:  "Documentation",
				Negate: true,
			},
		},
	}
}

func buildTeamMap(bugs []*bugzilla.Bug, orgData *teams.OrgData) (map[string][]*bugzilla.Bug, error) {
	out := map[string][]*bugzilla.Bug{}
	for _, team := range orgData.Teams {
		out[team.Name] = []*bugzilla.Bug{}
	}
	out["unknown"] = []*bugzilla.Bug{}

	for i := range bugs {
		bug := bugs[i]
		team := orgData.GetTeam(bug)
		out[team] = append(out[team], bug)
	}

	return out, nil
}

func getBugzillaAccess(cmd *cobra.Command) (bugzilla.Client, bugzilla.Query, error) {
	query := bugzilla.Query{}
	client, err := BugzillaClient(cmd)
	if err != nil {
		return client, query, err
	}
	query = getAllOpenBugsQuery()
	return client, query, nil
}

func (bd *BugData) Reconciler() {
	go func() {
		for true {
			if err := bd.reconcile(); err != nil {
				bd.errs <- err
				return
			}
			time.Sleep(time.Minute * 5)
		}
	}()
}

func GetBugData(cmd *cobra.Command, orgData *teams.OrgData, errs chan error) (*BugData, error) {
	client, query, err := getBugzillaAccess(cmd)
	if err != nil {
		return nil, err
	}
	bugData := &BugData{
		cmd:     cmd,
		errs:    errs,
		client:  client,
		query:   query,
		orgData: orgData,
	}
	err = bugData.reconcile()
	if err != nil {
		return nil, err
	}
	return bugData, nil
}

func AddFlags(cmd *cobra.Command) {
	cmd.Flags().String(bugDataFlagName, bugDataFlagDefVal, bugDataFlagUsage)
	cmd.Flags().String(APIKeyFlagName, apiKeyFlagDefVal, apiKeyFlagUsage)
}
