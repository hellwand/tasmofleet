---
section: Changed
---
Start warning when a sign-in variable (`AUTH_MODE`, `PUBLIC_URL`, `OIDC_*`, `ADMIN_GROUPS`, `OPERATOR_GROUPS`, `OIDC_CLIENT_SECRET_FILE`) is set in the environment without the `TASMOFLEET_` prefix: such a variable was ignored without a word; the warning names the key that works, the value is still ignored and never logged (#721)
