// Package domain groups the business core: money, wallet, wagering, events and
// ident. Its packages depend only on the standard library and on each other,
// never on Fx, HTTP, SQS or persistence libraries (DOM-07). This is enforced by
// depguard and by TestDomainHasNoInfraImports.
package domain
