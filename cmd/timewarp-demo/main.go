package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/timewarp-dev/timewarp/internal/demo"
	"github.com/timewarp-dev/timewarp/pkg/sdk"
)

func main() {
	collectorURL := flag.String("collector", "http://localhost:7777", "Timewarp collector URL")
	checkoutAddr := flag.String("checkout-addr", ":7780", "checkout demo listen address")
	paymentAddr := flag.String("payment-addr", ":7781", "payment demo listen address")
	flag.Parse()

	paymentURL := "http://localhost" + *paymentAddr
	recorder := sdk.NewClient(
		sdk.WithEndpoint(*collectorURL),
		sdk.WithService("checkout"),
		sdk.WithInstance("demo"),
	)
	checkout := demo.NewCheckout(recorder, paymentURL, &http.Client{Timeout: 2 * time.Second})

	go func() {
		log.Printf("demo payment listening on %s", *paymentAddr)
		log.Fatal(http.ListenAndServe(*paymentAddr, demo.PaymentHandler()))
	}()
	log.Printf("demo checkout listening on %s", *checkoutAddr)
	log.Printf("try: curl -X POST http://localhost%s/checkout -d '{\"amount\":4200}'", *checkoutAddr)
	log.Fatal(http.ListenAndServe(*checkoutAddr, checkout.Handler()))
}
