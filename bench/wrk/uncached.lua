-- Defeat the page cache on every request: each one carries a query string no
-- previous request used, so the response must come from PHP and the database.
-- Without this the "uncached" scenario silently measures the cache again.
local counter = 0

request = function()
  counter = counter + 1
  return wrk.format(nil, wrk.path .. "?bench=" .. counter .. "-" .. math.random(1, 1e9))
end
