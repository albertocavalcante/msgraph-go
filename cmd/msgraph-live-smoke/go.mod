module github.com/albertocavalcante/msgraph-go/cmd/msgraph-live-smoke

go 1.26

require (
	github.com/albertocavalcante/msauth-go v0.0.0
	github.com/albertocavalcante/msgraph-go v0.0.0
)

require (
	al.essio.dev/pkg/shellescape v1.5.1 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.7.2 // indirect
	github.com/danieljoos/wincred v1.2.2 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/zalando/go-keyring v0.2.6 // indirect
	golang.org/x/sys v0.26.0 // indirect
)

replace github.com/albertocavalcante/msauth-go => ../../../msauth-go

replace github.com/albertocavalcante/msgraph-go => ../..
