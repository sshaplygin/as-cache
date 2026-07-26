package redis

import goredis "github.com/redis/go-redis/v9"

// syncScript publishes one replica's counts, optionally claims the bucket, and
// reads back whatever decision has been published for it - in one round trip,
// because every replica makes this call on every coordination epoch and it is
// the only one that scales with fleet size.
//
// The bucket comes from the server's clock. That is the whole reason this is a
// script rather than a pipeline: if each replica divided its own clock into
// buckets, a fleet would need clock agreement finer than its coordination
// epoch, and one machine with a skewed clock would write its counts into a
// window nobody reads while dragging the fleet's view of its traffic with it.
// Asking the server means there is one clock and no agreement to reach.
//
//	KEYS[1]    anchor key, present so Cluster routes the script to the slot
//	           the computed keys live in
//	ARGV[1]    key base, including the hash tag: "asc:{namespace}"
//	ARGV[2]    coordination epoch in milliseconds
//	ARGV[3]    counter TTL in milliseconds
//	ARGV[4]    leader TTL in milliseconds
//	ARGV[5]    node id
//	ARGV[6]    "1" to claim leadership of the bucket
//	ARGV[7...] alternating field name and delta
var syncScript = goredis.NewScript(`
local base = ARGV[1]
local epoch = tonumber(ARGV[2])

local t = redis.call('TIME')
local nowMs = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local bucket = math.floor(nowMs / epoch)

if #ARGV >= 7 then
	local countsKey = base .. ':c:' .. bucket
	for i = 7, #ARGV, 2 do
		redis.call('HINCRBY', countsKey, ARGV[i], ARGV[i + 1])
	end
	-- Refreshed by every writer, so a bucket outlives its last contribution
	-- rather than its first.
	redis.call('PEXPIRE', countsKey, ARGV[3])
end

local leader = 0
if ARGV[6] == '1' then
	if redis.call('SET', base .. ':l:' .. bucket, ARGV[5], 'NX', 'PX', ARGV[4]) then
		leader = 1
	end
end

-- An empty string rather than a nil: a nil inside a Lua table terminates the
-- array Redis builds from it, so returning one here would truncate the reply
-- and take the bucket and leader values with it.
local decision = redis.call('GET', base .. ':d:' .. bucket)
if not decision then
	decision = ''
end

return {bucket, leader, decision}
`)

// decideScript publishes the decision for a bucket if none has been published,
// and returns whichever decision is in force either way.
//
// It returns rather than acknowledges because the leader has to act on the
// same policy as everyone following it. A leader that applied its own draw
// while the fleet applied an earlier one would be the one replica running
// something different, and it would be the one making the decisions.
//
//	KEYS[1] anchor key, for Cluster routing
//	ARGV[1] key base, including the hash tag
//	ARGV[2] bucket
//	ARGV[3] policy
//	ARGV[4] decision TTL in milliseconds
var decideScript = goredis.NewScript(`
local key = ARGV[1] .. ':d:' .. ARGV[2]

if redis.call('SET', key, ARGV[3], 'NX', 'PX', ARGV[4]) then
	return ARGV[3]
end

return redis.call('GET', key)
`)
