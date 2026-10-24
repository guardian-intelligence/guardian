// Every line on the upload page, Bengali first with English beneath.
export const STRINGS = {
  title: ["ভিডিও পাঠান", "Send videos"],
  intro: [
    "ফোনের গ্যালারি থেকে খামারের ভিডিও বাছাই করুন। ভিডিওগুলো সরাসরি Anveio-তে চলে যাবে।",
    "Pick farm videos from your phone's gallery. They go straight to Anveio.",
  ],
  name: ["আপনার নাম", "Your name"],
  stream: ["কিসের ভিডিও?", "What kind of video?"],
  drone: ["ড্রোন", "Drone"],
  headcam: ["মাথার ক্যামেরা", "Head camera"],
  car: ["গাড়ি", "Car"],
  other: ["অন্যান্য", "Other"],
  choose: ["ভিডিও বাছাই করুন", "Choose videos"],
  needName: ["আগে আপনার নাম লিখুন", "Write your name first"],
  keepOpen: [
    "সব ভিডিও পাঠানো শেষ না হওয়া পর্যন্ত এই পেজটি খোলা রাখুন।",
    "Keep this page open until every video is sent.",
  ],
  resume: [
    "কোনো ভিডিও মাঝপথে থেমে গেলে সেটি আবার বাছাই করুন — যেখানে থেমেছিল সেখান থেকে চলবে।",
    "If a video stops partway, choose it again — it picks up where it stopped.",
  ],
  waiting: ["অপেক্ষায়", "Waiting"],
  uploading: ["পাঠানো হচ্ছে", "Sending"],
  offline: ["নেটওয়ার্কের অপেক্ষায়", "Waiting for network"],
  finishing: ["শেষ হচ্ছে", "Finishing"],
  done: ["পাঠানো হয়েছে", "Sent"],
  failed: ["পাঠানো যায়নি", "Couldn't send"],
  sent: ["পাঠানো ভিডিও", "Sent videos"],
  checking: ["লিংক যাচাই করা হচ্ছে…", "Checking link…"],
  badLink: [
    "এই লিংকটি আর কাজ করছে না। নতুন লিংকের জন্য যোগাযোগ করুন।",
    "This link no longer works. Ask for a new one.",
  ],
} as const satisfies Record<string, readonly [string, string]>;

export type StringKey = keyof typeof STRINGS;
