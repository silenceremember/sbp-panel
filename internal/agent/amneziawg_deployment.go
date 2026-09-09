package agent

import (
	"encoding/json"
	"strings"
)

const amneziaWGMetadataPath = "/opt/vpn-panel-managed/amneziawg/server.json"

// Bump when the generated server configuration changes. Engine changes are
// tracked separately, so every managed deployment uses one replacement path.
const amneziaWGDeploymentRevision = "1"

func amneziaWGDeploymentCurrent(body []byte) bool {
	var metadata map[string]string
	if json.Unmarshal(body, &metadata) != nil {
		return false
	}
	return metadata["engine"] == awgVersion && metadata["deployment_revision"] == amneziaWGDeploymentRevision
}

func amneziaWGInstalledVersion(body []byte) string {
	var metadata map[string]string
	if json.Unmarshal(body, &metadata) != nil {
		return "Unverified deployment"
	}
	engine := strings.TrimSpace(metadata["engine"])
	if engine == "" {
		return "Installed (engine unrecorded)"
	}
	return "3.1 (engine " + engine + ")"
}
