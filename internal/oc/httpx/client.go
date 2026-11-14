// Package httpx vendor 自 opencode2api（MIT，https://github.com/jasonxu114514/opencode2api）。
// V2UserAgent 报告为 OpenCode 2.x CLI 的 UA 形状。
package httpx

// v2ClientVersion is the OpenCode 2.x CLI release the gateway claims to be on
// the v2 lanes. Upstream accepts a range of releases (the version number
// itself is not validated), so this only needs bumping when it starts
// rejecting older ones.
const v2ClientVersion = "2.1.1"

// v1BunVersion is the Bun runtime the v1 release ships with (kept for the
// vendored Bun transport's v1 UA helper).
const v1BunVersion = "1.3.14"

// V2UserAgent reports the gateway as an OpenCode 2.x CLI using the shape the
// v2 gate expects: "opencode/<channel>/<version>/<name>".
func V2UserAgent() string {
	return "opencode/latest/" + v2ClientVersion + "/cli"
}
