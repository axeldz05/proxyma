module proxyma/main

go 1.22.0

replace proxyma/storage => ../storage

require (
	github.com/stretchr/testify v1.10.0
	proxyma/storage v0.0.0-00010101000000-000000000000
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
