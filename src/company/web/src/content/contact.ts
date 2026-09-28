// Contact. No form. One email address that goes to a real human.

// The one inbound address. Every other role (sales, press, security,
// careers) shares it rather than fanning out to mailboxes nobody reads.
export const CONTACT_EMAIL = "contact@guardianintelligence.org";

export const CONTACT_META = {
  title: "Contact — Guardian",
  description:
    "One email address for sales, press, security, and careers. No form. We answer every note.",
} as const;

export const contact = {
  kicker: "We answer every note.",
  hero: "Contact Guardian.",
  intro:
    "There is no form. One address reaches a person, whatever the note is about — sales, press, security, or careers. We try to answer within one working day.",
  email: CONTACT_EMAIL,
  mailingAddress:
    "Guardian Intelligence is operated by Anveio Foundation · Seattle, Washington, USA",
} as const;
