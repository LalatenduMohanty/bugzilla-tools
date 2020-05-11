package bugs

import (
	"io/ioutil"
	"strings"
	"sync"
	"time"

	"github.com/eparis/bugzilla"
	"github.com/eparis/react-material/pkg/teams"
	"github.com/spf13/cobra"
)

const (
	UpcomingSprint   = "UpcomingSprint"
	apiKeyFlagName   = "bugzilla-key"
	apiKeyFlagDefVal = "bugzillaKey"
	apiKeyFlagUsage  = "Path to file containering BZ API key"

	bugDataFlagName   = "test-bug-data"
	bugDataFlagDefVal = ""
	bugDataFlagUsage  = "Path to file containing test bug data"
)

type BugMap map[string][]*bugzilla.Bug

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

type BugData struct {
	sync.RWMutex
	bugs   []*bugzilla.Bug
	bugMap BugMap
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

func (bd *BugData) reconcile(client bugzilla.Client, query bugzilla.Query, teams teams.Teams) error {
	bugs, err := client.Search(query)
	if err != nil {
		return err
	}
	bugMap, err := buildTeamMap(bugs, teams)
	if err != nil {
		return err
	}
	bd.set(bugs, bugMap)
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

func bugzillaClient(cmd *cobra.Command) (bugzilla.Client, error) {
	if testPath, err := cmd.Flags().GetString(bugDataFlagName); err != nil {
		return nil, err
	} else if testPath != "" {
		return bugzilla.GetTestClient(testPath), nil
	}

	endpoint := "https://bugzilla.redhat.com"

	keyFile, err := cmd.Flags().GetString(apiKeyFlagName)
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

func getNotUpcomingSprintQuery() bugzilla.Query {
	return bugzilla.Query{
		Classification: []string{"Red Hat"},
		Product:        []string{"OpenShift Container Platform"},
		Status:         []string{"NEW", "ASSIGNED", "POST", "ON_DEV"},
		//Component:      []string{"Networking", "Etcd", "Management Console"},
		IncludeFields: []string{"id", "summary", "status", "severity", "target_release", "component", "sub_components", "keywords"},
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

func buildTeamMap(bugs []*bugzilla.Bug, teams teams.Teams) (map[string][]*bugzilla.Bug, error) {
	out := map[string][]*bugzilla.Bug{}
	for _, team := range teams.Teams {
		out[team.Name] = []*bugzilla.Bug{}
	}
	out["unknown"] = []*bugzilla.Bug{}

	for i := range bugs {
		bug := bugs[i]
		team := teams.GetTeam(bug.Component[0])
		out[team] = append(out[team], bug)
	}

	return out, nil
}

func getBugzillaAccess(cmd *cobra.Command) (bugzilla.Client, bugzilla.Query, error) {
	query := bugzilla.Query{}
	client, err := bugzillaClient(cmd)
	if err != nil {
		return client, query, err
	}
	query = getNotUpcomingSprintQuery()
	return client, query, nil
}

func ReconcileBugData(cmd *cobra.Command, teams teams.Teams, bugData *BugData) error {
	client, query, err := getBugzillaAccess(cmd)
	if err != nil {
		return err
	}
	err = bugData.reconcile(client, query, teams)
	if err != nil {
		return err
	}
	return nil
}

func BugDataReconciler(errs chan error, cmd *cobra.Command, teams teams.Teams, bugData *BugData) {
	client, query, err := getBugzillaAccess(cmd)
	if err != nil {
		errs <- err
		return
	}
	go func() {
		for true {
			if err := bugData.reconcile(client, query, teams); err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Minute * 5)
		}
	}()
}

func AddFlags(cmd *cobra.Command) {
	cmd.Flags().String(bugDataFlagName, bugDataFlagDefVal, bugDataFlagUsage)
	cmd.Flags().String(apiKeyFlagName, apiKeyFlagDefVal, apiKeyFlagUsage)
}
