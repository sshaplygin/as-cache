package redis

import (
	"strconv"
	"strings"

	ascache "github.com/sshaplygin/as-cache"
	"github.com/sshaplygin/as-cache/bandit"
)

// DefaultKeyPrefix is what every key this package writes begins with.
const DefaultKeyPrefix = "asc"

// keyBase is the common prefix of every key belonging to a namespace,
// including the hash tag.
//
// The braces are what put a whole namespace in one Redis Cluster slot. That
// matters twice: the window read is a pipeline across many bucket keys, and
// the scripts compute key names themselves - a bucket comes from the server's
// clock and so cannot be known when the call is made, and Cluster refuses a
// script access outside the slot its declared keys hash to.
func (s *Store) keyBase(namespace string) string {
	return s.prefix + ":{" + namespace + "}"
}

// anchorKey is the key handed to a script as KEYS[1], so Cluster routes it to
// the slot the computed keys live in. Nothing is ever stored under it.
func (s *Store) anchorKey(namespace string) string {
	return s.keyBase(namespace) + ":anchor"
}

// countsKey holds one bucket's per-arm counters as a hash. It must match the
// expression the Lua builds.
func (s *Store) countsKey(namespace string, bucket bandit.Bucket) string {
	return s.keyBase(namespace) + ":c:" + strconv.FormatInt(int64(bucket), 10)
}

// countField names one counter within a bucket's hash.
//
// The policy is written as its numeric PolicyType rather than its name. Names
// come from stringer and are a presentation detail: renaming one would
// silently split a fleet's counters in two, with every replica still reporting
// and none of them agreeing, and nothing in the data would show why. The role
// is "a" for active or "s" for shadow, and the suffix is "h" for hits or "m"
// for misses.
func countField(policy ascache.PolicyType, role bandit.Role, hits bool) string {
	kind := "m"
	if hits {
		kind = "h"
	}

	roleTag := "s"
	if role == bandit.RoleActive {
		roleTag = "a"
	}

	return strconv.FormatUint(uint64(policy), 10) + ":" + roleTag + ":" + kind
}

// parseCountField reverses countField. An unrecognised field is reported as
// not ok rather than as an error: the store may be shared, and a stray field
// written by something else must not stop a fleet reading its own counters.
func parseCountField(field string) (policy ascache.PolicyType, role bandit.Role, hits, ok bool) {
	parts := strings.Split(field, ":")
	if len(parts) != 3 {
		return 0, 0, false, false
	}

	number, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false, false
	}

	switch parts[1] {
	case "a":
		role = bandit.RoleActive
	case "s":
		role = bandit.RoleShadow
	default:
		return 0, 0, false, false
	}

	switch parts[2] {
	case "h":
		hits = true
	case "m":
		hits = false
	default:
		return 0, 0, false, false
	}

	return ascache.PolicyType(number), role, hits, true
}
