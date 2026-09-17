package provider

import "fmt"

// German user-facing strings. Code, attribute names and comments stay English;
// everything an operator reads in a plan or an error is German, because the
// rights vocabulary they know comes from the ChurchTools UI.
const (
	orphanOnDeleteSummary = "Objekt bleibt in ChurchTools bestehen"
	notConfiguredSummary  = "Provider nicht konfiguriert"
)

// orphanOnDeleteDetail explains what Delete actually did.
//
// This provider NEVER deletes in ChurchTools. Removing a resource from the
// configuration un-manages it — exactly what `ct state rm` does in ct-cli —
// and leaves the object untouched on the instance. Refusing Delete outright
// was the first design; it makes a resource impossible to un-manage without
// hand-editing state, and it breaks `tofu destroy` on a scratch workspace.
//
// Accidental removal is guarded elsewhere and more precisely: `prevent_destroy`
// on every generated resource, plus a CI gate that fails any plan containing a
// delete action.
func orphanOnDeleteDetail(kind string) string {
	return fmt.Sprintf(
		"%s werden von diesem Provider nie in ChurchTools gelöscht. Die Ressource wurde nur "+
			"aus dem Terraform-State entfernt; das Objekt existiert in ChurchTools weiter. "+
			"Lösche es dort von Hand, falls es wirklich weg soll.",
		kind,
	)
}
