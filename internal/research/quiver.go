package research

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxCatalogEntries = 4096
const maxCatalogAssets = 4096

type quiverIndex struct {
	Version int `json:"version"`
	Lists   []struct {
		ID string `json:"id"`
	} `json:"lists"`
	PlatformMetadataURL string `json:"platformMetadataUrl"`
}

type quiverPlatform struct {
	FormatRevision int                   `json:"formatRevision"`
	Entries        []quiverPlatformEntry `json:"entries"`
}

type quiverPlatformEntry struct {
	Provider          string          `json:"provider"`
	Repository        string          `json:"repository"`
	ReleaseTag        string          `json:"releaseTag"`
	PreferredRelease  json.RawMessage `json:"preferredRelease"`
	AssetNames        []string        `json:"assetNames"`
	SelectionRevision int             `json:"selectionRevision"`
}

type quiverCatalog struct {
	Name string          `json:"name"`
	Apps json.RawMessage `json:"apps"`
}

type quiverApp struct {
	Name               string   `json:"name"`
	Project            string   `json:"project"`
	Repository         string   `json:"repository"`
	FolderName         string   `json:"folderName"`
	AppIconURL         string   `json:"appIconUrl"`
	Tags               []string `json:"tags"`
	RepositorySource   string   `json:"repositorySource"`
	CatalogID          string   `json:"catalogId"`
	ReleaseAssetFilter string   `json:"releaseAssetFilter"`
	Group              string   `json:"-"`
}

// A non-null preferredRelease value is deliberately not interpreted until its
// schema is confirmed by the adapter. Treating an unknown object as a release
// would turn untrusted metadata into a false equality comparison.
func preferredReleaseTag(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", true
	}
	return "", false
}

// decodeQuiverMetadata is the adapter boundary. It validates only the fixed
// declarative Quiver metadata shape; comparison and rendering remain outside
// this file.
func decodeQuiverMetadata(data map[string][]byte, files []string) (quiverIndex, quiverPlatform, []quiverApp, error) {
	if len(files) != 6 {
		return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("invalid Quiver metadata file set")
	}
	for _, name := range files {
		if _, ok := data[name]; !ok {
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("missing Quiver metadata file %s", name)
		}
	}
	var index quiverIndex
	if err := json.Unmarshal(data[files[0]], &index); err != nil {
		return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("index: %w", err)
	}
	if index.Version != 2 || len(index.Lists) != 4 || index.PlatformMetadataURL == "" {
		return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("unsupported Quiver index version")
	}
	listIDs := make(map[string]bool, len(index.Lists))
	for _, list := range index.Lists {
		if list.ID == "" || listIDs[list.ID] {
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("invalid Quiver catalog list metadata")
		}
		listIDs[list.ID] = true
	}
	var platform quiverPlatform
	if err := json.Unmarshal(data[files[1]], &platform); err != nil {
		return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("platform-index: %w", err)
	}
	if platform.FormatRevision != 1 || platform.Entries == nil {
		return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("unsupported Quiver platform-index revision")
	}
	if len(platform.Entries) > maxCatalogEntries {
		return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("Quiver platform entry count exceeds limit")
	}
	for _, record := range platform.Entries {
		if len(record.AssetNames) > maxCatalogAssets {
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("Quiver asset count exceeds limit")
		}
		for _, name := range record.AssetNames {
			if name == "" {
				return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("Quiver asset filename is empty")
			}
		}
	}
	var apps []quiverApp
	for _, name := range files[2:] {
		var catalog quiverCatalog
		if err := json.Unmarshal(data[name], &catalog); err != nil {
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("%s: %w", name, err)
		}
		if catalog.Name == "" || len(catalog.Apps) == 0 || string(catalog.Apps) == "null" {
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("%s: catalog group or apps array is missing", name)
		}
		var catalogApps []quiverApp
		if err := json.Unmarshal(catalog.Apps, &catalogApps); err != nil || catalogApps == nil {
			if err == nil {
				err = fmt.Errorf("apps array is null")
			}
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(catalogApps) > maxCatalogEntries-len(apps) {
			return quiverIndex{}, quiverPlatform{}, nil, fmt.Errorf("Quiver application count exceeds limit")
		}
		for _, app := range catalogApps {
			app.Group = catalog.Name
			apps = append(apps, app)
		}
	}
	return index, platform, apps, nil
}
