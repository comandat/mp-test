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

---

**→ Partea a 2-a (HTTP handlers, UI templates, Dockerfile, Railway config, security notes, verificare, listă ștergeri) urmează într-un fișier separat sau în continuarea acestuia.**
