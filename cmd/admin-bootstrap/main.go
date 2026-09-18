// admin-bootstrap issues the only initial Super Admin invitation. It refuses to
// create staff once an account exists. Renewal only replaces the original,
// never-accepted invitation. The output is a secret; distribute it securely.
package main

import (
	"booking/go-server/internal/admin"
	"context"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"os"
	"time"
)

func main() {
	email := flag.String("email", "", "First Super Admin email")
	name := flag.String("name", "", "First Super Admin name")
	renew := flag.Bool("renew-invitation", false, "Replace the original, never-accepted invitation")
	reason := flag.String("reason", "", "Required audit reason for invitation renewal")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	svc, err := admin.New(pool, admin.Config{PublicURL: os.Getenv("ADMIN_PUBLIC_URL"), EncryptionKeys: os.Getenv("ADMIN_MFA_ENCRYPTION_KEYS"), ActiveKey: os.Getenv("ADMIN_MFA_ACTIVE_KEY"), BcryptCost: 12}, nil)
	if err != nil {
		log.Fatal(err)
	}
	var link string
	if *renew {
		link, err = svc.RenewBootstrapInvitation(ctx, *email, *reason)
	} else {
		link, err = svc.Bootstrap(ctx, *email, *name)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(link)
}
