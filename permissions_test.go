package msgraph

import "testing"

func TestFindPermission(t *testing.T) {
	permission, ok := FindPermission("mail.readwrite")
	if !ok {
		t.Fatal("Mail.ReadWrite not found")
	}
	if permission.ID != MailReadWriteDelegatedID {
		t.Fatalf("ID = %q, want %q", permission.ID, MailReadWriteDelegatedID)
	}
	if !permission.PersonalAccountsAllowed {
		t.Fatal("Mail.ReadWrite should be marked valid for personal accounts")
	}

	permission, ok = FindPermission(MailSendDelegatedID)
	if !ok {
		t.Fatal("Mail.Send ID not found")
	}
	if permission.Name != "Mail.Send" {
		t.Fatalf("Name = %q, want Mail.Send", permission.Name)
	}
}
