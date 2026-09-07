package freshness

import "testing"

func TestLoadRepositories(t *testing.T) {
	value, err := LoadRepositories([]byte("- app: demo\n  repository: owner/project\n"))
	if err != nil {
		t.Fatal(err)
	}
	if value["demo"] != "owner/project" {
		t.Fatalf("repository = %q", value["demo"])
	}
}

func TestLoadRepositoriesRejectsMalformedData(t *testing.T) {
	for _, data := range []string{
		"- app: demo\n  repository: owner/project\n- app: demo\n  repository: other/project\n",
		"- app: demo\n  repository: https://github.com/owner/project\n",
		"- app: demo\n  repository: owner/project\n---\n- app: other\n  repository: owner/other\n",
	} {
		if _, err := LoadRepositories([]byte(data)); err == nil {
			t.Fatalf("accepted malformed metadata %q", data)
		}
	}
}

func TestRepositoryPreservesReviewedMappings(t *testing.T) {
	for _, test := range []struct {
		app        string
		repository string
	}{
		{app: "pcsx2", repository: "PCSX2/pcsx2"},
		{app: "dusklight", repository: "TwilitRealm/dusklight"},
	} {
		repository, ok, err := Repository(test.app)
		if err != nil || !ok || repository != test.repository {
			t.Fatalf("Repository(%q) = %q, %t, %v", test.app, repository, ok, err)
		}
	}
	if _, ok, err := Repository("not-approved"); err != nil || ok {
		t.Fatalf("unknown repository mapping = ok:%t err:%v", ok, err)
	}
}
