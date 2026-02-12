module helpdesk

go 1.25.5

require (
	github.com/VantageDataChat/GoExcel v0.0.0-20260210221956-22a34d8dea7f
	github.com/VantageDataChat/GoPDF2 v0.0.0-20260210221934-debe2ff9c48d
	github.com/VantageDataChat/GoPPT v0.0.0-20260210220934-e91ef3c4e651
	github.com/VantageDataChat/GoWord v0.0.0-20260210220908-40c2b82002d1
	github.com/mattn/go-sqlite3 v1.14.34
	github.com/nicexipi/sqlite-vec v0.0.0
	golang.org/x/crypto v0.48.0
	golang.org/x/image v0.36.0
	golang.org/x/oauth2 v0.35.0
	golang.org/x/sys v0.41.0
	pgregory.net/rapid v1.2.0
)

require (
	github.com/phpdave11/gofpdi v1.0.15 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	go.mozilla.org/pkcs7 v0.9.0 // indirect
	golang.org/x/text v0.34.0 // indirect
)

replace github.com/nicexipi/sqlite-vec => ./sqlite-vec
