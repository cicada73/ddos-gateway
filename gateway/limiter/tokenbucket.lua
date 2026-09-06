-- Atomic token bucket check-and-consume, run entirely inside Redis.
--
-- Why Lua here: two gateway replicas could otherwise both read "3 tokens
-- left", both decide to allow, and both write "2 tokens left" - silently
-- letting through one more request than the bucket should permit. Redis
-- executes a Lua script as a single atomic operation, so there's no window
-- for another replica's request to interleave with this one.
--
-- Why redis.call('TIME') instead of passing time from Go: every gateway
-- replica has its own system clock, and clock drift between machines would
-- corrupt the refill math. Using Redis's own clock gives every replica a
-- single, consistent source of time.
--
-- KEYS[1] = bucket key, e.g. "ratelimit:ip:203.0.113.7"
-- ARGV[1] = capacity (max tokens)
-- ARGV[2] = refill rate (tokens per second)
-- ARGV[3] = requested tokens for this call (1 per request)
-- ARGV[4] = idle TTL in seconds, so buckets for clients who've stopped
--           making requests eventually expire instead of accumulating in
--           Redis forever.

local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local requested = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local time = redis.call('TIME')
local now = tonumber(time[1]) + (tonumber(time[2]) / 1000000)

local bucket = redis.call('HMGET', key, 'tokens', 'timestamp')
local tokens = tonumber(bucket[1])
local timestamp = tonumber(bucket[2])

if tokens == nil then
    -- First time we've seen this key: start with a full bucket.
    tokens = capacity
    timestamp = now
end

local elapsed = math.max(0, now - timestamp)
local filled = math.min(capacity, tokens + (elapsed * refill_rate))

local allowed = 0
local new_tokens = filled
if filled >= requested then
    allowed = 1
    new_tokens = filled - requested
end

redis.call('HMSET', key, 'tokens', new_tokens, 'timestamp', now)
redis.call('EXPIRE', key, ttl)

return { allowed, tostring(new_tokens) }