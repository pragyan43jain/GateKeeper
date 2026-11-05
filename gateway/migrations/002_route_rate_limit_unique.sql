CREATE UNIQUE INDEX IF NOT EXISTS rate_limit_rules_route_policy_unique_idx
    ON rate_limit_rules(route_id)
    WHERE route_id IS NOT NULL AND api_key_id IS NULL AND enabled;
