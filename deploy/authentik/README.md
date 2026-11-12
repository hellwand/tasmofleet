# Authentik setup for tasmofleet

Example deployment for Authentik. The menu paths and names in the provider sections below
are Authentik's; another identity provider has its own. tasmofleet is a confidential OIDC client
(authorization code flow with PKCE); roles come from the `groups` claim.

## Groups

| Group               | tasmofleet role | Variable                                      |
| ------------------- | --------------- | --------------------------------------------- |
| `Tasmofleet Admins` | admin           | `TASMOFLEET_ADMIN_GROUPS=Tasmofleet Admins`   |
| `Tasmofleet Users`  | operator        | `TASMOFLEET_OPERATOR_GROUPS=Tasmofleet Users` |

A user in none of the groups gets a 403 page and no session.

## OAuth2/OpenID provider

| Setting             | Value                                                                    |
| ------------------- | ------------------------------------------------------------------------ |
| Client type         | Confidential                                                             |
| Redirect URI        | `https://tasmofleet.example.com/oidc/callback` (strict match)          |
| Signing key         | an RSA certificate, so ID tokens are RS256                               |
| Scopes              | `openid`, `profile`, `email` (`profile` carries `groups` in Authentik)   |
| Subject mode        | based on the user's hashed ID (stable `sub`)                             |
| Issuer              | per provider: `https://auth.example.com/application/o/tasmofleet/`     |
| Grant types         | `authorization_code` and `refresh_token`                                 |

tasmofleet also requests `offline_access` to get a refresh token. If the provider does not
issue one, sessions are capped to the re-check interval (`TASMOFLEET_OIDC_RECHECK`, 15 minutes) and the
start log says so; select the `offline_access` scope mapping on the provider.

Providers created through the API need `grant_types` set explicitly to
`authorization_code` and `refresh_token`; the UI does this for you.

Create an application `tasmofleet` (slug `tasmofleet`, so the issuer above matches),
bind the two groups to it, and restrict access through those bindings.

## tasmofleet side

Set `TASMOFLEET_PUBLIC_URL` (container configuration; only the environment or the env file can
set it), then fill in the rest under System, Settings, tab "Sign-in" (admin, needs your
password), and press "Test discovery". The values are stored in the database; they take effect
after a restart, and local accounts keep working if anything is wrong.

Alternatively set them in the container configuration (the page then shows them read-only,
with the badge "Environment"):

```
TASMOFLEET_AUTH_MODE=oidc
TASMOFLEET_PUBLIC_URL=https://tasmofleet.example.com
TASMOFLEET_OIDC_ISSUER=https://auth.example.com/application/o/tasmofleet/
TASMOFLEET_OIDC_CLIENT_ID=<from the provider>
TASMOFLEET_OIDC_CLIENT_SECRET_FILE=/data/oidc-client-secret
TASMOFLEET_ADMIN_GROUPS=Tasmofleet Admins
TASMOFLEET_OPERATOR_GROUPS=Tasmofleet Users
```

Every variable in the container environment needs the `TASMOFLEET_` prefix: a name without it
is ignored, without any message. The same keys without the prefix are valid only as lines in
the env file `/data/tasmofleet.env`. See [configuration](../../docs/configuration.md).

Each setting has one source, the page or the container configuration, never both. A value from
the environment wins on every start, is never copied into the database, and a save from the page
that names it is refused. The issuer and the client secret lock together: set
`TASMOFLEET_OIDC_ISSUER` and the client secret in the same place (both as variables, or both on
the page). With only the secret in the environment, the issuer saved on the page is ignored
and sign-in falls back to local accounts at the next start. A client secret entered on the page
is bound to the issuer's scheme, host and port; after the issuer moves to another host, enter it
again. After changing a variable, re-create the container (`docker compose up -d`);
a restart keeps the old environment.

Never put the client secret into a file in this repository.

## Header mode (alternative)

Use a proxy provider with an Authentik outpost behind Traefik forward auth
(see `deploy/traefik/tasmofleet.yml`), `TASMOFLEET_AUTH_MODE=header`, and
`TASMOFLEET_TRUSTED_PROXIES` set to the Traefik host. Headers are honoured only from that
peer address.

## Application icon (optional)

This step is optional and only concerns Authentik: it sets what Authentik shows for the
application. In Authentik, set the application's icon (Admin -> Applications -> tasmofleet ->
Icon) to `assets/branding/icon-512.png` from this repository (upload the file, or use
`https://raw.githubusercontent.com/hellwand/tasmofleet/main/assets/branding/icon-512.png`).

tasmofleet neither sends nor reads an icon, a logo or any other client metadata, and its web UI
always shows its own icon. Skipping the step, or setting an icon or logo on the client of
another identity provider, changes nothing in tasmofleet.
