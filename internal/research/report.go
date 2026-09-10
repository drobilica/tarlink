package research

import (
	"net/url"
	"sort"
	"strings"
)

// RenderCandidateReport renders the local candidate ledger without consulting
// the network or adding time-dependent data to the output.
func RenderCandidateReport(ledger CandidateLedger) string {
	var b strings.Builder
	b.WriteString("# TarLink Registry Candidates\n\n")
	b.WriteString("Generated from `registry-research/candidates.yaml`.\n\n")
	b.WriteString("`candidates.yaml` is authoritative. Do not edit this generated report.\n\n")

	groups := make(map[string][]Candidate)
	for _, candidate := range ledger.Candidates {
		groups[candidateReportGroup(candidate)] = append(groups[candidateReportGroup(candidate)], candidate)
	}
	order := []string{
		"Ready / Potential candidates", "Deferred / Needs investigation",
		"Missing runtime libraries", "Windows only", "No Linux artifact", "Source only",
		"Setup script required", "System integration required", "Other blocked",
		"Rejected / Out of scope",
	}
	for _, name := range order {
		candidates := groups[name]
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
		b.WriteString("## " + name + "\n\n")
		renderCandidateTable(&b, candidates)
	}
	return b.String()
}

func candidateReportGroup(candidate Candidate) string {
	if candidate.Status == "ready" {
		return "Ready / Potential candidates"
	}
	if candidate.Status == "deferred" {
		return "Deferred / Needs investigation"
	}
	if candidate.Status == "rejected" {
		return "Rejected / Out of scope"
	}
	for _, blocker := range blockerGroupOrder {
		for _, candidateBlocker := range candidate.Blockers {
			if candidateBlocker == blocker.id {
				return blocker.title
			}
		}
	}
	return "Other blocked"
}

type blockerGroup struct{ id, title string }

var blockerGroupOrder = []blockerGroup{
	{"MISSING_RUNTIME_LIBS", "Missing runtime libraries"},
	{"WINDOWS_ONLY", "Windows only"},
	{"NO_LINUX_ARTIFACT", "No Linux artifact"},
	{"SOURCE_ONLY", "Source only"},
	{"SETUP_SCRIPT_REQUIRED", "Setup script required"},
	{"SYSTEM_INTEGRATION_REQUIRED", "System integration required"},
}

func renderCandidateTable(b *strings.Builder, candidates []Candidate) {
	b.WriteString("| Candidate | Upstream | Last checked | Blockers | Evidence | Notes |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, candidate := range candidates {
		row := []string{
			candidate.ID,
			repoMarkdownLink(candidate.Upstream),
			releaseMarkdownLink(candidate.Upstream, candidate.LastChecked.ReleaseTag),
			strings.Join(sortedStrings(candidate.Blockers), ", "),
			evidenceMarkdown(candidate.EvidenceLinks),
			candidate.Notes,
		}
		b.WriteString("| ")
		for index, cell := range row {
			if index > 0 {
				b.WriteString(" | ")
			}
			if index == 1 || index == 2 || index == 4 {
				b.WriteString(markdownEscapeLinkCell(cell))
			} else {
				b.WriteString(markdownEscapeCell(cell))
			}
		}
		b.WriteString(" |\n")
	}
	b.WriteString("\n")
}

func repoMarkdownLink(raw string) string {
	repo, err := ParseRepository(raw)
	if err != nil {
		return raw
	}
	return markdownLink(string(repo), "https://github.com/"+string(repo))
}

func releaseMarkdownLink(rawRepo, tag string) string {
	repo, err := ParseRepository(rawRepo)
	if err != nil {
		return tag
	}
	return markdownLink(tag, "https://github.com/"+string(repo)+"/releases/tag/"+url.PathEscape(tag))
}

func evidenceMarkdown(links []EvidenceLink) string {
	values := make([]string, 0, len(links))
	for _, link := range links {
		values = append(values, markdownLink(link.Label, link.URL))
	}
	return strings.Join(values, ", ")
}

func markdownLink(text, href string) string {
	return "[" + markdownEscapeLinkText(text) + "](" + strings.NewReplacer("(", "%28", ")", "%29").Replace(href) + ")"
}

func markdownEscapeLinkText(value string) string {
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)").Replace(value)
}

func markdownEscapeCell(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "<br>")
}

func markdownEscapeLinkCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "<br>")
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
