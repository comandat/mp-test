# Plan implementare — eMAG Order Tracker (Go + Railway)

## Context

Momentan repoul `comandat/mp-test` conține un proiect Python (Flask + Camoufox) pentru automatizare creare conturi eMAG — nelegat de ceea ce vrem acum. Îl **golim complet** și construim o aplicație Go nouă pe branch-ul `claude/protonmail-email-filter-P4HVx`.

Ce construim: un serviciu Go care se conectează direct la contul ProtonMail via `github.com/ProtonMail/go-proton-api` (fără Bridge), scanează periodic inbox-ul pentru email-uri eMAG, extrage datele comenzilor (produse livrate de eMAG, PIN-uri easybox, QR-uri, deadline-uri), și expune un UI web mobile-friendly hostat pe Railway. Urmărim 3 stări per comandă: `Înregistrată` → `În livrare` → `Gata de ridicare`, cu buton de confirmare care ascunde comanda după ridicare. UI-ul are pe ecranul principal un buton spre o hartă Leaflet cu pin-uri la fiecare easybox pentru planificarea traseului.

## Decizii confirmate cu userul

- **Credențiale Proton**: env vars pe Railway (`PROTON_USERNAME`, `PROTON_PASSWORD`, `PROTON_MAILBOX_PASSWORD`, `PROTON_TOTP_SECRET`) — aplicația generează TOTP singură la login.
- **Auth UI**: fără autentificare. URL-ul Railway e public — user-ul acceptă riscul și nu îl partajează.
- **Hartă**: Leaflet + OpenStreetMap tiles + Nominatim pentru geocoding (respectăm 1 req/sec, cache în SQLite).
- **Confirmă ridicarea**: marchează doar local (`picked_up_at = now`), filtrat din listă. NU șterge / mută email-ul pe Proton.

## 1. Layout proiect

```
/home/user/mp-test
├── cmd/server/main.go              # entrypoint: wires config, db, proton sync, http
├── internal/
│   ├── config/config.go            # env var loading
│   ├── proton/
│   │   ├── client.go               # login/refresh, AddAuthHandler persistence
│   │   ├── keyring.go              # salt + mailbox-password unlock
│   │   ├── sync.go                 # NewEventStream loop + initial backfill
│   │   └── fetch.go                # GetMessage, GetAttachment, MIME walker
│   ├── parser/
│   │   ├── common.go               # goquery helpers, Romanian number/date parsers
│   │   ├── confirmation.go         # email type 1 (Confirmare înregistrare comandă)
│   │   ├── shipped.go              # email type 2 (a fost predată curierului)
│   │   ├── arrived.go              # email type 3 (a ajuns în easybox)
│   │   └── testdata/*.eml          # fixtures pentru unit tests
│   ├── store/
│   │   ├── db.go                   # modernc.org/sqlite + migrations
│   │   ├── orders.go               # CRUD + state transitions
│   │   ├── events.go               # last_event_id, processed_message_ids
│   │   └── session.go              # proton_session (uid, refresh_token)
│   ├── geocode/
│   │   ├── nominatim.go            # rate-limited 1 req/sec
│   │   └── cache.go                # sqlite-backed lookup
│   ├── web/
│   │   ├── server.go               # chi router, middleware
│   │   ├── handlers.go             # list/detail/confirm/map/img/health
│   │   └── templates.go            # html/template via embed.FS
│   └── model/types.go              # Order, Product, OrderStatus enum
├── web/
│   ├── templates/                  # layout.html, list.html, detail.html, map.html
│   └── static/                     # app.css + Leaflet assets
├── Dockerfile
├── railway.json
├── go.mod / go.sum
└── .gitignore
```

## 2. Persistență — SQLite pe Railway Volume

Folosim `modernc.org/sqlite` (pure Go, fără CGO → imagine Docker distroless). Railway Volume mount la `/data`, env `DB_PATH=/data/app.db`.

Schema (migrations inline în `store/db.go`, rulate la startup):

```sql
CREATE TABLE orders(
  order_number TEXT PRIMARY KEY,
  status TEXT NOT NULL,            -- Inregistrata | InLivrare | GataDeRidicare
  easybox_name TEXT, easybox_address TEXT,
  pickup_deadline TEXT,            -- RFC3339
  pin_code TEXT,
  qr_attachment_id TEXT,           -- FK → attachments
  total_bani INTEGER,              -- x100 ca să evităm float
  picked_up_at TEXT,               -- NULL = încă vizibil
  created_at TEXT, updated_at TEXT
);
CREATE TABLE products(
  id INTEGER PRIMARY KEY,
  order_number TEXT REFERENCES orders,
  name TEXT, image_url TEXT, qty INTEGER, line_total_bani INTEGER,
  seller_group TEXT                -- "eMAG" sau "<seller> via eMAG"
);
CREATE TABLE emails_processed(
  message_id TEXT PRIMARY KEY, kind TEXT, order_number TEXT, processed_at TEXT
);
CREATE TABLE attachments(
  id TEXT PRIMARY KEY, order_number TEXT, content_type TEXT, bytes BLOB
);
CREATE TABLE geocode_cache(
  address TEXT PRIMARY KEY, lat REAL, lon REAL, fetched_at TEXT
);
CREATE TABLE proton_session(
  id INTEGER PRIMARY KEY CHECK(id=1),
  uid TEXT, refresh_token TEXT, last_event_id TEXT
);
```

## 3. Sync loop ProtonMail

Startup flow în `proton.Client.Start`:

1. Citim `proton_session`. Dacă `uid+refresh_token` există → `manager.NewClientWithRefresh(ctx, uid, rt)`. La eșec → fresh login.
2. Fresh login: `manager.NewClientWithLogin(ctx, user, pass)`. Dacă `auth.TwoFA.Enabled&proton.HasTOTP != 0` → generăm TOTP din `PROTON_TOTP_SECRET` cu `github.com/pquerna/otp/totp` → `c.Auth2FA(ctx, proton.Auth2FAReq{TwoFactorCode: code})`.
3. Unlock keyring: `salts, _ := c.GetSalts(ctx)` → `salt := salts.SaltForKey(primaryKeyID)` → `user.Keys.Unlock(saltedKey, nil)`. Stocăm keyring pe client pentru decriptare mesaje.
4. `c.AddAuthHandler(func(a proton.Auth){ store.SaveSession(a.UID, a.RefreshToken) })` — salvează la rotație token.
5. Initial backfill la primul start: `eventID, _ := c.GetLatestEventID(ctx)`; paginare `c.GetMessageMetadata(ctx, proton.MessageFilter{LabelID: proton.InboxLabel, Subject: "comand"})` → acoperă "Comanda" + "comandă", filtrăm fin în Go cu regex per subject.
6. Steady-state: `stream := c.NewEventStream(ctx, 60*time.Second, 5*time.Second, lastEventID)`. Pentru fiecare `evt.Messages` cu `EventCreate` pe Inbox → dispatch la classifier. Persistăm `evt.EventID` după procesare.

Fetch & MIME walk în `proton/fetch.go`:
- `msg, _ := c.GetMessage(ctx, id)` → bytes MIME decriptați.
- `github.com/emersion/go-message/mail` iterează părțile. `text/html` → body; `image/*` cu `Content-ID` → QR inline. Dacă QR-ul e attachment separat → `c.GetAttachment(ctx, attID)`. Stocat în `attachments` keyed pe UUID, referențiat din `orders.qr_attachment_id`.

## 4. Parsere email (`internal/parser`)

Folosim `github.com/PuerkitoBio/goquery`. Fiecare parser: `(html, subject string) → (*Parsed, error)`.

**Clasificator** (în `sync.go`):
- Subject match `^Confirmare înregistrare comandă #(\d+)` → `confirmation`
- Subject match `^Comanda ta #(\d+) a fost predată curierului` → `shipped`
- Body match `^Hei,\s*Comanda ta eMAG numărul (\d+) a ajuns în` → `arrived`
- Respinge explicit `Comanda ta eMAG Marketplace - .+?,eMAG numărul` (marketplace, nu livrat de eMAG)

**confirmation.go**: număr comandă din subject. Găsește ancora `Produse livrate de eMAG`, iterează secțiunile. Acceptă headere `Produse vândute de eMAG` SAU `Produse vândute de <SELLER> și livrate de eMAG`. Respinge `Produse vândute și livrate de <non-eMAG>` (ex. Unitel). Per produs: `img[src]`, nume (text `a`), qty (`\d+\s*buc`), preț rând (`([\d.,]+)\s*Lei`). Total general la finalul grupului.

**shipped.go**: același extractor produs; setează `Status=InLivrare`. În acest tip de email grupurile sunt deja doar `vândute și livrate de eMAG` sau `vândute de X și livrate de eMAG` — ambele sunt livrate de eMAG.

**arrived.go**:
- Reject dacă body conține `Comanda ta eMAG Marketplace - .+?,eMAG numărul`.
- Order num după `numărul `.
- Deadline după `pana ` — parse zi RO (`Luni`..`Duminica`), dată (`22 Apr.`), oră (`3:30`). Map lună RO (`Ian,Feb,Mar,Apr,Mai,Iun,Iul,Aug,Sep,Oct,Noi,Dec`). Anul curent; dacă data parsată e în trecut → year+1.
- PIN: după textul `Sau tastează pe ecranul easybox codul:`, ia următoarele 7 linii non-goale, primul char din fiecare → concatenează (ex. `Y\nL\n4\nU\nT\n6\nC` → `YL4UT6C`).
- Easybox name: linia după `păstrare.` (skip `pin_easybox` placeholder).
- Address: linia următoare (ex. `Str. Dambovitei, Nr. 30`).
- QR: găsește imaginea inline cu `Content-ID` referențiat în `<img src="cid:...">` lângă blocul PIN.

Parsere pure, unit-testate cu `testdata/*.eml` (capturăm email-uri reale, le salvăm anonymized).

## 5. State machine comenzi

States: `Inregistrata`, `InLivrare`, `GataDeRidicare`, `Ridicata` (soft, via `picked_up_at IS NOT NULL`).

Tranziții (idempotente, ordonate după timestamp email ca să prevină regresii):

| Curent | Email type 1 (conf) | Email type 2 (shipped) | Email type 3 (arrived) | User confirm |
|---|---|---|---|---|
| (nou) | → Inregistrata | → InLivrare (stub) | → GataDeRidicare | — |
| Inregistrata | update products | → InLivrare | → GataDeRidicare | — |
| InLivrare | update products | update products | → GataDeRidicare | — |
| GataDeRidicare | ignore | ignore | refresh deadline/PIN/QR | → Ridicata |
| Ridicata | ignore | ignore | ignore | — |

Nu merge înapoi niciodată. Enforced în `store.orders.Upsert` prin compare pe ordinal.

## 6. HTTP server & routes

Folosim `github.com/go-chi/chi/v5` (lean, stdlib-style). Template-uri `html/template` incluse via `embed.FS` → binarul final e self-contained, nu trimitem fișiere suplimentare în Docker image.

Rute:

| Metodă | Path | Descriere |
|---|---|---|
| GET | `/` | Listă comenzi cu `picked_up_at IS NULL`, sortate: `GataDeRidicare` sus (urgent), apoi după deadline, apoi după `created_at` |
| GET | `/order/{num}` | Pagina detaliu — produse, PIN mare, deadline, QR, buton "Confirma ridicarea" |
| POST | `/order/{num}/confirm` | Setează `picked_up_at=now()`, redirect 303 `/` |
| GET | `/map` | Pagină Leaflet full-viewport |
| GET | `/api/pins` | JSON array `[{order_number, lat, lon, easybox_name, deadline}]` pentru markerele de pe hartă |
| GET | `/img/{attID}` | Servește bytes din `attachments` cu `Content-Type` + `Cache-Control: private, max-age=86400` |
| GET | `/healthz` | `200 ok` — Railway healthcheck |

Listen pe `:${PORT}` (default 8080). Middleware: `chi/middleware.Recoverer`, `RequestID`, `Logger`, plus middleware custom care adaugă `X-Robots-Tag: noindex`, `Referrer-Policy: no-referrer`, `Content-Security-Policy: default-src 'self'; img-src 'self' data: https://*.emag.ro; style-src 'self' 'unsafe-inline'; script-src 'self'`.

## 7. UI wireframe (mobile-first)

**`list.html`** — viewport meta `width=device-width, initial-scale=1`, font system (`-apple-system, Segoe UI, Roboto`). Single-column stack de carduri:
- Status pill color-coded: `Inregistrata`=gri, `InLivrare`=albastru, `GataDeRidicare`=verde pulsat
- `#485475037` big
- Total lei (bold, 20px+)
- Deadline relativ via `<time>` + JS mic: `în 6h 12m` / `EXPIRAT`
- Easybox name small grey
- Tap pe card → `/order/{num}`

Buton fix jos: `Vezi harta` (full-width, CTA primary) → `/map`. Folosim `position: fixed; bottom: 0` cu `padding-bottom: env(safe-area-inset-bottom)` pentru iPhone. Listă are `padding-bottom: 80px` ca să nu ascundă ultimul card.

QR și butonul confirmă **NU** apar pe listă — doar pe detail.

**`detail.html`** — header cu back arrow → `/`, număr comandă, status pill. Apoi:
- Easybox name + address (adresa tappable `<a href="geo:{lat},{lon}?q={encoded_address}">` — deschide nativ Google Maps / Apple Maps)
- QR mare centrat (`width: min(80vw, 320px)`), alt text = PIN
- PIN în font monospace mare, cu buton `Copiază` (JS `navigator.clipboard.writeText`)
- Deadline cu countdown live
- Listă produse (thumbnail 60x60 + nume + qty + preț)
- Total plată la easybox
- Footer sticky: buton `Confirma ridicarea` → form POST, `onsubmit="return confirm('Ești sigur că ai ridicat coletul?')"`

**`map.html`** — Leaflet 1.9 full-viewport. Tile OSM: `https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png` cu atribuirea obligatorie. Fetch `/api/pins`, `L.marker([lat,lon]).bindPopup('<a href="/order/485...">#485...</a><br>deadline')`. Bounds auto-fit cu `map.fitBounds(markers)`. Back button sus-stânga → `/`.

## 8. Geocoding (Nominatim)

`internal/geocode/nominatim.go`:
- Client HTTP cu `User-Agent: emag-tracker/1.0 (contact via env NOMINATIM_USER_AGENT)` — obligatoriu conform ToS Nominatim.
- Rate limit strict: `time.Tick(1100 * time.Millisecond)` — max 1 req/sec.
- Request: `GET https://nominatim.openstreetmap.org/search?q={address}&format=json&limit=1&countrycodes=ro`.
- Response → `lat`, `lon` → salvat în `geocode_cache(address, lat, lon, fetched_at)`.
- Cache check înainte de orice request → majoritatea hit-urilor vin din DB.
- Dacă Nominatim întoarce 0 rezultate sau eroare → marcăm `lat=NULL, lon=NULL` și retry pe următorul sync (nu spam). Pin-ul nu apare pe hartă până când geocoding-ul reușește.

Trigger: după ce `arrived.go` extrage o adresă nouă, enqueue pe canal `chan string` consumat de worker-ul geocode.

## 9. Dockerfile (multi-stage, fără CGO)

```dockerfile
# syntax=docker/dockerfile:1.6

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
```

Pure Go SQLite (`modernc.org/sqlite`) → nu avem nevoie de libc → distroless static. Templates și assets statice sunt în binar via `embed.FS`.

## 10. Railway config

**`railway.json`** (root):

```json
{
  "build": { "builder": "DOCKERFILE", "dockerfilePath": "Dockerfile" },
  "deploy": {
    "startCommand": "/server",
    "healthcheckPath": "/healthz",
    "healthcheckTimeout": 30,
    "restartPolicyType": "ON_FAILURE",
    "restartPolicyMaxRetries": 10
  }
}
```

**Env vars** de setat în Railway dashboard:
- `PROTON_USERNAME`
- `PROTON_PASSWORD`
- `PROTON_MAILBOX_PASSWORD`
- `PROTON_TOTP_SECRET` — Base32 secret din QR-ul 2FA (scanat manual o dată)
- `DB_PATH=/data/app.db`
- `NOMINATIM_USER_AGENT=emag-tracker/1.0 (email@exemplu.ro)`
- `PORT` — injectat automat de Railway
- `SYNC_INTERVAL=60s` (optional, default 60s)

**Volume**: atașat la `/data`, min 1 GB (mai mult decât suficient pentru SQLite + attachments QR).

`go.mod` pin go-proton-api la un commit SHA fix (librăria nu are tag-uri de release) — pinning-ul e important pentru reproducibilitate.

## 11. Note securitate

User-ul a ales explicit **no auth** pe UI. Riscuri:
- Oricine cu URL-ul Railway vede PIN-urile easybox + QR-urile → poate ridica coletele.
- URL-urile Railway sunt generate random (`*.up.railway.app`) → greu de ghicit, dar NU secrete (apar în logs, SNI, TLS, browser history).

Mitigări minime aplicate:
- `X-Robots-Tag: noindex` + `robots.txt: Disallow: /` → nu ajunge în Google.
- `Referrer-Policy: no-referrer` → URL-ul nu scapă prin click-uri externe.
- CSP strict.

**Recomandare (NU implementăm acum, dar e ușor de adăugat ulterior)**: middleware de shared-secret — tot ce nu e `/healthz` verifică cookie-ul `s=<env>`. Un handler `GET /login?k=<env>` setează cookie-ul HttpOnly. Costă ~30 linii Go și rezolvă 99% din risc. Flag-uit în README.

## 12. Plan verificare (end-to-end)

**Local dev**:
```bash
cp .env.example .env        # completează cu credențiale test
go run ./cmd/server
# browser: http://localhost:8080
```

**Unit tests parsere** (critice — logica cea mai fragilă):
1. Salvezi 3 email-uri reale din Proton ca `.eml` (export manual sau capture raw din API).
2. Anonimizezi datele (PIN-uri, order numbers) dar păstrezi structura HTML.
3. Pui în `internal/parser/testdata/{confirmation,shipped,arrived}_*.eml`.
4. `go test ./internal/parser/...` verifică extracția: număr comandă, produse, qty, prețuri, PIN, deadline, easybox name+address.
5. Adaugă fixture pentru edge cases: (a) comandă mixtă eMAG + Unitel (ignoră Unitel), (b) comandă mixtă `vândut de eMAG` + `vândut de X și livrat de eMAG` (ține ambele), (c) email `Marketplace ... ,eMAG` (respinge complet).

**Smoke integration**:
- Cont Proton throwaway + trimis manual 3 email-uri HTML copiate din Proton real.
- Pornit binarul → verificat `sqlite3 app.db "SELECT order_number, status FROM orders"`.
- Verificat că un al doilea email (shipped) actualizează statusul fără să dubleze rândul.
- Click pe `Confirma ridicarea` → rând cu `picked_up_at` setat, dispărut din listă.

**Mobile UI check**:
- Chrome DevTools device mode: iPhone SE (375×667), iPhone 14 Pro (393×852), Pixel 7 (412×915).
- Tap targets ≥ 44×44 px.
- Throttling 3G → pagina listă < 1s, detail < 1.5s.
- `/map` pe mobile: verificat că pinch-to-zoom merge, markere clickable cu degetul.

**Railway deploy**:
- Push pe `claude/protonmail-email-filter-P4HVx` → Railway auto-deploy.
- Watch logs pentru: `db: migrated`, `proton: session restored` sau `proton: fresh login OK`, `proton: event stream connected from <eventID>`, `geocode: cached <address>`.
- Hit `https://<app>.up.railway.app/healthz` → `ok`.
- Hit `/` → listă populată după ~1 min.

## 13. Fișiere de șters din repo existent

**Șterge complet**:
```
main.py
app.py
config.py
account_creator.py
browser_engine.py
sms_provider.py
storage.py
email_generator.py
local_worker.py
test_nopecha.py
extensions/               (director complet — nopecha extension)
templates/                (director complet — Flask templates)
requirements.txt
nixpacks.toml
Dockerfile                (rescris pentru Go)
accounts.json
__pycache__/              (dacă există)
duckduckgo_api.md
herosms_api.md
emag_generator.log        (dacă există)
nopecha_test.png          (dacă există)
```

**Păstrează**:
- `.git/`
- `.gitignore` (rescris pentru Go: `/server`, `*.db`, `*.db-journal`, `.env`, `dist/`, `vendor/`)

## 14. Fișiere noi critice

Ordinea recomandată de implementare (fiecare e testabil independent):

1. `go.mod` + `go.sum` — dependențe pinned
2. `internal/config/config.go` — env vars
3. `internal/store/db.go` + `orders.go` + `session.go` — persistență (testabil izolat)
4. `internal/parser/*.go` + `testdata/` — parsere pure (testabil izolat, cel mai important — aici e toată valoarea extracției)
5. `internal/proton/client.go` + `keyring.go` — auth flow
6. `internal/proton/sync.go` + `fetch.go` — event stream + classifier
7. `internal/geocode/nominatim.go` + `cache.go`
8. `internal/web/server.go` + `handlers.go` + `templates.go`
9. `web/templates/*.html` + `web/static/app.css`
10. `cmd/server/main.go` — wiring final
11. `Dockerfile` + `railway.json` + `.gitignore` + `.env.example`

## 15. Funcții `go-proton-api` folosite (referință)

Auth/session:
- `proton.New()` → `*Manager`
- `manager.NewClientWithLogin(ctx, username, password)` → `(*Client, Auth, error)`
- `manager.NewClientWithRefresh(ctx, uid, refreshToken)` → `(*Client, Auth, error)`
- `client.Auth2FA(ctx, Auth2FAReq{TwoFactorCode})`
- `client.AddAuthHandler(func(Auth))` — pentru persistare token rotit
- `client.GetSalts(ctx)` + `salt.SaltForKey(keyID)`
- `client.GetUser(ctx)` + `user.Keys.Unlock(key, nil)`

Messages:
- `client.GetLatestEventID(ctx)`
- `client.NewEventStream(ctx, pollInterval, jitter, fromEventID)` → `<-chan Event`
- `client.GetMessageMetadata(ctx, MessageFilter{LabelID: InboxLabel, Subject: "comand"})`
- `client.GetMessage(ctx, messageID)` → MIME decriptat
- `client.GetAttachment(ctx, attachmentID)` → bytes decriptați

Constante/tipuri:
- `proton.InboxLabel`
- `proton.MessageFilter`
- `proton.Event`, `proton.EventCreate`, `proton.EventUpdate`

Dependențe suport:
- `github.com/emersion/go-message/mail` — parsare MIME
- `github.com/PuerkitoBio/goquery` — parsare HTML
- `github.com/pquerna/otp/totp` — generare TOTP
- `modernc.org/sqlite` — SQLite pure Go
- `github.com/go-chi/chi/v5` — router HTTP

---

**Plan complet.** După aprobare: ștergem fișierele din §13, creăm scheletul Go din §1, implementăm în ordinea din §14. Estimare: 3–4 sesiuni de lucru pentru MVP funcțional (parsere + sync + UI minimal), încă 1 sesiune pentru Dockerfile + Railway + hartă.
