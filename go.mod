module github.com/comandat/mp-test

go 1.23.0

toolchain go1.24.7

require (
	github.com/ProtonMail/go-proton-api v0.4.0
	github.com/ProtonMail/gopenpgp/v2 v2.4.10
	github.com/PuerkitoBio/goquery v1.10.3
	github.com/pquerna/otp v1.4.0
	modernc.org/sqlite v1.34.1
)

require (
	github.com/ProtonMail/bcrypt v0.0.0-20211005172633-e235017c1baf // indirect
	github.com/ProtonMail/gluon v0.13.1-0.20221025093924-86bbf0261eb8 // indirect
	github.com/ProtonMail/go-crypto v0.0.0-20220824120805-4b6e5c587895 // indirect
	github.com/ProtonMail/go-mime v0.0.0-20220429130430-2192574d760f // indirect
	github.com/ProtonMail/go-srp v0.0.5 // indirect
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/bradenaw/juniper v0.8.0 // indirect
	github.com/cloudflare/circl v1.2.0 // indirect
	github.com/cronokirby/saferith v0.33.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/emersion/go-message v0.16.0 // indirect
	github.com/emersion/go-textwrapper v0.0.0-20200911093747-65d896831594 // indirect
	github.com/emersion/go-vcard v0.0.0-20220507122617-d4056df0ec4a // indirect
	github.com/go-resty/resty/v2 v2.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/sirupsen/logrus v1.8.1 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/exp v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/net v0.39.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/sys v0.32.0 // indirect
	golang.org/x/text v0.24.0 // indirect
	modernc.org/gc/v3 v3.0.0-20240107210532-573471604cb6 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)

replace github.com/ProtonMail/go-proton-api => ./third_party/go-proton-api
