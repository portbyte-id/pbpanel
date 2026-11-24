package waf

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/portbyte-id/server-panel/internal/nginx"
)

// open-appsec as a second engine. Everything below is rendered from measured
// facts on the agent, not from its documentation; docs/design/open-appsec-plan.md
// §6 and docs/design/open-appsec-step4-findings.md carry the evidence.
//
// Two pieces of configuration belong to the engine and nothing else:
//
//   - a main-context file that loads the attachment module, and the per-server
//     `cp-nano-nginx-attachment on;` that makes a site inspected. The directive is
//     unknown to nginx unless the module is loaded (plan §0.5), so the two are
//     always written and removed in the same change. That is why the module is
//     loaded only while at least one site uses the engine: every other site then
//     needs no directive at all, and there is no second state to keep in step.
//   - the agent's local policy, which names every open-appsec site by host.
const (
	// OpenAppsecModuleFile is the attachment module, as `load_module` takes it:
	// relative to the build's own prefix, like the ModSecurity module.
	OpenAppsecModuleFile = nginx.AttachmentModuleFile

	// OpenAppsecDirective turns inspection on for one server. Its default is
	// inspected, so it is written only where it is wanted; a site without it is
	// never inspected while the module is loaded, because the module is loaded
	// only for sites that want it.
	OpenAppsecDirective = "cp-nano-nginx-attachment on;"

	// openAppsecDefaultPractice is the stock practice of an installed agent.
	openAppsecDefaultPractice = "default-threat-prevention-practice"
	openAppsecDefaultACL      = "default-access-control-practice"
	openAppsecDefaultTrigger  = "default-log-trigger"
	openAppsecDefaultResponse = "default-web-user-response"

	// openAppsecSchema is the policy schema the installed agent reads
	// (`based on schema version: "v1beta2"` in its own header).
	openAppsecSchema = "v1beta2"
)

// OpenAppsecModulePath is where the module loader lives: beside the ModSecurity
// one, in the directory panel-main.conf globs.
func (e *Engine) OpenAppsecModulePath() string {
	return path.Join(e.p.WAFMainDir(), "open-appsec.conf")
}

// OpenAppsecPolicyFile is the local policy the agent applies. It belongs to the
// agent but is written through confedit like every other managed config, so a
// rejected policy rolls back the way a rejected `nginx -t` does.
func (e *Engine) OpenAppsecPolicyFile() string {
	return path.Join(e.p.WAFDir(), "open-appsec", "local_policy.yaml")
}

// OpenAppsecMainInclude is the main-context file that loads the module.
func OpenAppsecMainInclude() []byte {
	return []byte("# Managed by PB panel (open-appsec).\n" +
		"# Loaded only while a site uses open-appsec: the per-server directive is\n" +
		"# unknown to nginx without it, so the two are always written together.\n" +
		"load_module " + OpenAppsecModuleFile + ";\n")
}

// writeOpenAppsecBlock writes the per-server directive. Everything else in the
// block (rate limit, paths, IP rules, the challenge) is engine-independent and
// is written by the same code for both engines.
func writeOpenAppsecBlock(b *strings.Builder) {
	b.WriteString("# Inspected by open-appsec; its policy is waf/open-appsec/local_policy.yaml.\n")
	b.WriteString(OpenAppsecDirective + "\n")
}

// OpenAppsecPolicy is one open-appsec site as the policy names it.
type OpenAppsecPolicy struct {
	// Hosts are the server names the site answers to: the domain and its
	// aliases, in that order.
	Hosts []string
	Mode  Mode
	// Confidence is the lowest threat score that blocks, when Mode is block.
	Confidence Confidence
}

// RenderOpenAppsecPolicy renders the agent's local policy from every open-appsec
// site, sorted by host so an unchanged set of sites renders byte for byte the
// same file and an apply writes nothing.
//
// The shape is the one measured to compile on the installed agent: the default
// practice is `inactive`, so only the hosts named in specificRules are acted on,
// and every practice carries all four blocks the schema requires. A practice
// without them is accepted by --apply-policy and then silently discarded; see
// docs/design/open-appsec-step4-findings.md §A.
//
// Exceptions are deliberately not rendered. No exception key is proven to work
// on this agent: path exceptions have no working key at all (plan §6.1), and the
// named-exception form compiled empty in step 4's probes (§C, same file). A
// control that silently does nothing is worse than one that is absent, so the
// policy carries none.
func RenderOpenAppsecPolicy(sites []OpenAppsecPolicy) []byte {
	// A site with no host names would render a rule with an empty host, which the
	// agent compiles into a match on nothing; and a specificRules with no entries
	// at all is not YAML. Both are dropped here, which also makes the empty case
	// the `specificRules: []` below.
	ordered := make([]OpenAppsecPolicy, 0, len(sites))
	for _, s := range sites {
		if len(s.Hosts) > 0 {
			ordered = append(ordered, s)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return firstHost(ordered[i]) < firstHost(ordered[j])
	})

	var b strings.Builder
	b.WriteString("# Managed by PB panel (open-appsec policy). Rewritten on every firewall change.\n")
	b.WriteString("# schema: " + openAppsecSchema + "\n")
	b.WriteString("# Hosts not listed below are inactive: only the sites in specificRules are\n")
	b.WriteString("# inspected, which is the second half of the per-server directive's promise.\n")
	b.WriteString("# Exceptions are absent on purpose: no open-appsec key is proven to work,\n")
	b.WriteString("# and a control that silently matches nothing is worse than no control.\n\n")
	b.WriteString("apiVersion: " + openAppsecSchema + "\n\n")

	b.WriteString("policies:\n")
	b.WriteString("  default:\n")
	b.WriteString("    mode: inactive\n")
	fmt.Fprintf(&b, "    threatPreventionPractices: [%s]\n", openAppsecDefaultPractice)
	fmt.Fprintf(&b, "    accessControlPractices: [%s]\n", openAppsecDefaultACL)
	fmt.Fprintf(&b, "    customResponses: %s\n", openAppsecDefaultResponse)
	fmt.Fprintf(&b, "    triggers: [%s]\n", openAppsecDefaultTrigger)
	b.WriteString("    sourceIdentifiers: \"\"\n")
	b.WriteString("    trustedSources: \"\"\n")
	b.WriteString("    exceptions: []\n")

	if len(ordered) == 0 {
		b.WriteString("  specificRules: []\n")
	} else {
		b.WriteString("  specificRules:\n")
		for _, s := range ordered {
			hosts := sortedHosts(s)
			for _, h := range hosts {
				fmt.Fprintf(&b, "    - name: %s\n", policyName(h))
				fmt.Fprintf(&b, "      host: %s\n", policyHost(h))
				fmt.Fprintf(&b, "      mode: %s\n", policyMode(s.Mode))
				fmt.Fprintf(&b, "      threatPreventionPractices: [%s]\n", confidencePractice(s.Confidence))
				fmt.Fprintf(&b, "      accessControlPractices: [%s]\n", openAppsecDefaultACL)
				fmt.Fprintf(&b, "      triggers: [%s]\n", openAppsecDefaultTrigger)
				b.WriteString("      sourceIdentifiers: \"\"\n")
				b.WriteString("      trustedSources: \"\"\n")
				b.WriteString("      exceptions: []\n")
			}
		}
	}

	b.WriteString("\nthreatPreventionPractices:\n")
	writePractice(&b, openAppsecDefaultPractice, ConfidenceHigh)
	// One practice per confidence the sites use, in a fixed order, so the file
	// does not depend on which site happened to be rendered first.
	for _, c := range []Confidence{ConfidenceCritical, ConfidenceMedium} {
		if usesConfidence(ordered, c) {
			writePractice(&b, confidencePractice(c), c)
		}
	}

	b.WriteString("\naccessControlPractices:\n")
	b.WriteString("  - name: " + openAppsecDefaultACL + "\n")
	b.WriteString("    practiceMode: inherited\n")
	b.WriteString("    rateLimit:\n")
	b.WriteString("      overrideMode: inherited\n")
	b.WriteString("      rules: []\n")

	b.WriteString("\nlogTriggers:\n")
	b.WriteString("  - name: " + openAppsecDefaultTrigger + "\n")
	b.WriteString("    accessControlLogging:\n")
	b.WriteString("      allowEvents: false\n")
	b.WriteString("      dropEvents: true\n")
	b.WriteString("    appsecLogging:\n")
	b.WriteString("      detectEvents: true\n")
	b.WriteString("      preventEvents: true\n")
	b.WriteString("      allWebRequests: false\n")
	b.WriteString("    extendedLogging:\n")
	b.WriteString("      urlPath: true\n")
	b.WriteString("      urlQuery: true\n")
	b.WriteString("      httpHeaders: false\n")
	b.WriteString("      requestBody: false\n")
	b.WriteString("    additionalSuspiciousEventsLogging:\n")
	b.WriteString("      enabled: true\n")
	b.WriteString("      minSeverity: high\n")
	b.WriteString("      responseBody: false\n")
	b.WriteString("      responseCode: true\n")
	b.WriteString("    logDestination:\n")
	b.WriteString("      cloud: false\n")
	b.WriteString("      logToAgent: true\n")
	b.WriteString("      stdout:\n")
	b.WriteString("        format: json\n")

	b.WriteString("\ncustomResponses:\n")
	b.WriteString("  - name: " + openAppsecDefaultResponse + "\n")
	b.WriteString("    mode: response-code-only\n")
	b.WriteString("    httpResponseCode: 403\n")
	return []byte(b.String())
}

// writePractice writes one threat-prevention practice. All four blocks are
// required by the schema; leaving one out makes the agent discard the whole
// policy while still reporting success, so they are always written.
func writePractice(b *strings.Builder, name string, c Confidence) {
	fmt.Fprintf(b, "  - name: %s\n", name)
	b.WriteString("    practiceMode: inherited\n")
	b.WriteString("    webAttacks:\n")
	b.WriteString("      overrideMode: inherited\n")
	fmt.Fprintf(b, "      minimumConfidence: %s\n", c)
	b.WriteString("    intrusionPrevention:\n")
	b.WriteString("      overrideMode: inherited\n")
	b.WriteString("      maxPerformanceImpact: medium\n")
	b.WriteString("      minSeverityLevel: medium\n")
	b.WriteString("      minCveYear: 2016\n")
	b.WriteString("      highConfidenceEventAction: inherited\n")
	b.WriteString("      mediumConfidenceEventAction: inherited\n")
	b.WriteString("      lowConfidenceEventAction: detect\n")
	b.WriteString("    fileSecurity:\n")
	b.WriteString("      overrideMode: inherited\n")
	b.WriteString("      minSeverityLevel: medium\n")
	b.WriteString("      highConfidenceEventAction: inherited\n")
	b.WriteString("      mediumConfidenceEventAction: inherited\n")
	b.WriteString("      lowConfidenceEventAction: detect\n")
	b.WriteString("    snortSignatures:\n")
	b.WriteString("      overrideMode: inherited\n")
	b.WriteString("      configmap: []\n")
	b.WriteString("      files: []\n")
}

// policyMode maps the panel's modes to the agent's. Only the two inspecting modes
// reach here: a site that is off is not listed.
func policyMode(m Mode) string {
	if m == ModeBlock {
		return "prevent-learn"
	}
	return "detect-learn"
}

// confidencePractice names the practice for one confidence level. The default
// practice's own minimumConfidence is high, so `high` reuses it.
func confidencePractice(c Confidence) string {
	if c == "" || c == ConfidenceHigh {
		return openAppsecDefaultPractice
	}
	return "pb-threat-prevention-" + string(c)
}

func usesConfidence(sites []OpenAppsecPolicy, c Confidence) bool {
	for _, s := range sites {
		if s.Confidence == c {
			return true
		}
	}
	return false
}

// sortedHosts is a site's server names, sorted, so the entry does not depend on
// the order the aliases were entered.
func sortedHosts(s OpenAppsecPolicy) []string {
	out := append([]string(nil), s.Hosts...)
	sort.Strings(out)
	return out
}

// firstHost is the site's domain, which is always first in Hosts.
func firstHost(s OpenAppsecPolicy) string {
	if len(s.Hosts) == 0 {
		return ""
	}
	return s.Hosts[0]
}

// policyName is the specificRules entry's name. sites.NormalizeDomain already
// limits a domain to letters, digits, dots and hyphens, so it is a safe token.
func policyName(host string) string { return "pb-" + host }

// policyHost writes the host the way YAML reads it back as a string. A bare
// domain is safe; anything else is quoted so a character YAML treats as syntax
// cannot change the meaning of the file.
func policyHost(host string) string {
	const unsafe = ":#{}[],&*?|-<>=!%@`\"' \t"
	if strings.ContainsAny(host, unsafe) {
		return "'" + strings.ReplaceAll(host, "'", "''") + "'"
	}
	return host
}
