package handlers

// maxPaymentLogMessage — had panjang defensif untuk `paymentlog.Entry.Message`
// (cth `err.Error()` gateway yang tak dijangka besar) — elak baris log
// tunggal yang melampau saiznya, padanan pola had panjang lain dalam pakej
// ni (cth donationCheckoutRequest.DonorName).
const maxPaymentLogMessage = 500

// truncateForLog pendekkan mesej ralat sebelum direkod paymentlog.Record —
// mesej ralat gateway pihak ketiga tak terkawal panjangnya.
func truncateForLog(s string) string {
	if len(s) <= maxPaymentLogMessage {
		return s
	}
	return s[:maxPaymentLogMessage] + "…(truncated)"
}
