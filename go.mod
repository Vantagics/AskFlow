module askflow

go 1.25.5

require (
	github.com/VantageDataChat/GoExcel v0.0.0-20260210221956-22a34d8dea7f
	github.com/VantageDataChat/GoPDF2 v0.0.0-20260212143022-4f8ad48dca6e
	github.com/VantageDataChat/GoPPT v0.0.0-20260221221545-3bb6889b1041
	github.com/VantageDataChat/GoWord v0.0.0-20260210220908-40c2b82002d1
	github.com/mattn/go-sqlite3 v1.14.34
	github.com/nicexipi/sqlite-vec v0.0.0
	github.com/richardlehane/mscfb v1.0.6
	github.com/shakinm/xlsReader v0.9.12
	golang.org/x/crypto v0.48.0
	golang.org/x/image v0.36.0
	golang.org/x/oauth2 v0.35.0
	golang.org/x/sys v0.41.0
)

require (
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728 // indirect
	github.com/metakeule/fmtdate v1.1.2 // indirect
	github.com/richardlehane/msoleps v1.0.3 // indirect
	go.mozilla.org/pkcs7 v0.9.0 // indirect
	golang.org/x/text v0.34.0 // indirect
)

replace github.com/nicexipi/sqlite-vec => ./sqlite-vec
