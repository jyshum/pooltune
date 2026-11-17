# Supabase consent redirect CSP focused review

Reviewed exact range `6c1487f19f2acca1c07f78a3d6ccb48956a4594e..2b8154b951464854920133e5b46602be5d4279d4`.

**Verdict: technically clean for this correction. No Critical, Important or Minor finding.** Previous whole-feature source findings remain resolved. The parent-reported live CSP blocker demonstrates that the prior local checks did not establish a complete browser flow.

The parent reported that the authorization API returns a 303 to `https://supabase.com/dashboard/authorize`, while Pooltune's form-action CSP allowed only `https://api.supabase.com`. Browser verification stopped on that redirect before consent was granted. I did not independently repeat external operations.

At `internal/hostedapp/app.go:179`, the change adds the exact HTTPS origin `https://supabase.com` to form-action only when Supabase is configured. It retains the API origin required earlier in the redirect chain. It adds no wildcard, insecure scheme or third-party origin, and changes no script/connect/frame permissions. Google-only and disabled behavior remain unchanged; enabling both providers retains both sets of destinations. The change is appropriate for the observed official consent destination.

The new test at `internal/hostedapp/security_headers_test.go:10` exercises the real ServeHTTP header construction for disabled, Google-only, Supabase-only and both-provider configurations. It asserts the entire resulting CSP, including the restrictive unrelated directives. The supplied red log failed in the two Supabase-enabled cases before the correction; the supplied green race log passed.

Independent exact-head archive check:

`go test -race ./internal/hostedapp -run TestOAuthFormActionAllowsOnlyEnabledProviderDestinations -count=1 -v`

All four cases passed in **1.443s**. Log: `/tmp/pooltune-supabase-csp-review-race.log`. Archive: `/tmp/pooltune-supabase-csp-review-2b8154b`.

No checkout/index/branch or external/customer state was changed. No agents were spawned. This review supports the header correction, not a claim that the browser has now completed consent. Root still owns repeat browser verification, strict writable provider proof and native setup. Deployment and human acceptance remain separate from this technical verdict.
