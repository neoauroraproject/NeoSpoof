# سرور لایسنس اسپوف — قرارداد API

این سند می‌گوید سرور لایسنس باید چه رفتاری داشته باشد تا با ماژول کلاینت داخل
`spoofd` (`license.go`) کار کند. کلاینت فقط **کلید عمومی** را دارد و توکن‌ها را
*اعتبارسنجی* می‌کند؛ **کلید خصوصی** فقط روی سرور توست و توکن‌ها را *امضا* می‌کند.

> صداقت امنیتی: نرم‌افزاری که روی سرور مشتری اجرا می‌شود «واقعاً غیرقابل‌کرک»
> نیست — باینری قابل پچ است. قدرت واقعی این طرح سمت سرور است: توکن‌های کوتاه‌عمرِ
> امضاشده + قفل IP + چک اجباری هر ۲۴ ساعت. جعل توکن بدون کلید خصوصی ممکن نیست.

## کلیدها

- **کلید عمومی** (داخل باینری، `licPubB64` در `license.go`):
  `ZRQrjfW3/ArVrEyMnlJEf4PLX2IIjeEbxUePCFlgxk4=`
- **کلید خصوصی** (فقط سرور): فایل `secrets/license_ed25519_private.key`
  (base64، ۶۴ بایت = seed(۳۲) + pub(۳۲)). این فایل در گیت نیست و نباید باشد.
  برای کتابخانه‌هایی مثل `@noble/ed25519` فقط **۳۲ بایت اول (seed)** لازم است.

اگر کلید خصوصی لو رفت: کلید جدید بساز، `licPubB64` را عوض کن، دوباره build کن.

## نقاط پایانی (Endpoints)

کلاینت این ۴ میزبان را (به‌صورت encode‌شده داخل باینری) به ترتیب امتحان می‌کند و
اولینی که جواب معتبر بدهد را می‌پذیرد. اگر مسیر مستقیم شکست بخورد و پروکسی SOCKS5
تنظیم شده باشد (`-license-proxy`)، همان درخواست را از پروکسی هم می‌زند.

- `https://license.hmray.pro/v1/license`
- `https://license.hmrayserver.com/v1/license`
- `https://license.hmrayapp.com/v1/license`
- `https://license.hossein-m466.workers.dev/v1/license`

هر ۴ باید رفتار یکسان داشته باشند و پایگاه‌داده‌ی bindها را به اشتراک بگذارند (یا
همگام شوند)، چون قفل IP باید روی همه‌شان یکی باشد. کافی‌ست لایسنس روی یکی ثبت/چک شود.

## درخواست

```
POST /v1/license
Content-Type: application/json

{ "key": "<license-key>", "nonce": "<hex-16-bytes>", "version": "1.0.0" }
```

IP واقعی سرورِ کلاینت = IP مبدأ همین درخواست (در Cloudflare: هدر `CF-Connecting-IP`).

## پاسخ موفق (۲۰۰)

```json
{
  "payload": "<base64(JSON claims)>",
  "sig": "<base64(ed25519 sign over the payload BYTES)>",
  "your_ip": "<IP مبدأ درخواست>"
}
```

`claims` که امضا می‌شود (دقیقاً همین بایت‌ها، بدون فاصله‌ی اضافه بعد از امضا):

```json
{
  "key": "<license-key>",
  "bound_ip": "<IP که لایسنس به آن قفل است>",
  "plan": "pro",
  "issued_at": 1730000000,
  "expires_at": 1730172800,
  "nonce": "<همان nonce درخواست>"
}
```

قواعد:

- **nonce** را عیناً برگردان (کلاینت تازگی پاسخ را با آن چک می‌کند).
- **issued_at** = زمان فعلی سرور (ثانیه)، **expires_at** = `issued_at + 48h`.
  کلاینت هر ۲۴ ساعت دوباره می‌گیرد؛ این پنجره یعنی یک چک ازدست‌رفته را تحمل می‌کند.
- **bound_ip**: اگر کلید تا حالا bind نشده، آن را به `your_ip` قفل کن و ذخیره کن.
  اگر قبلاً bind شده و `your_ip` فرق دارد → پاسخ خطا بده (زیر). همین «قفل IP» است
  که نمی‌گذارد اسکریپت روی سرور دیگری کار کند.
- `plan` هر رشته‌ای که می‌خواهی (`pro`, `trial`, ...). کلاینت فقط نمایش می‌دهد.

## پاسخ خطا (۴۰۰/۴۰۱/۴۰۳)

```json
{ "error": "<پیام فارسی که به کاربر نشان داده می‌شود>" }
```

نمونه‌ها:
- کلید ناشناخته/باطل → `{"error":"کلید لایسنس معتبر نیست"}`
- کلید قفل‌شده روی IP دیگر → `{"error":"این لایسنس به سرور دیگری قفل شده است"}`

## رفتار کلاینت (برای هماهنگی)

- امضا را با کلید عمومی چک می‌کند؛ `nonce` و تطبیق `bound_ip == your_ip` را بررسی
  می‌کند؛ بعد توکن امضاشده را روی دیسک (`data/license.json`) ذخیره می‌کند.
- «حالا» را به‌صورت **high-water mark** نگه می‌دارد (بیشینه‌ی ساعت سیستم و زمان‌های
  دیده‌شده)، پس عقب‌کشیدن ساعت عمر لایسنس را زیاد نمی‌کند.
- اگر هیچ endpoint در دسترس نبود، روی آخرین توکن کار می‌کند تا `expires_at`؛ بعد از
  آن **دی‌اکتیو** می‌شود (دکمه‌های تست/تانل ۴۰۳ می‌گیرند و صفحه‌ی فعال‌سازی می‌آید).

## نمونه‌ی مرجع: Cloudflare Worker

برای `license.hossein-m466.workers.dev`. از `@noble/ed25519` و KV برای bindها.

```js
// wrangler.toml:  kv_namespaces = [{ binding = "LICENSES", id = "..." }]
// secret:  wrangler secret put LICENSE_PRIVATE_KEY   (۶۴ بایت base64، مثل فایل secrets/)
// valid keys را هم یک‌جا نگه دار (KV یا لیست).
import * as ed from "@noble/ed25519";

const te = new TextEncoder();
const b64 = (u8) => btoa(String.fromCharCode(...u8));

export default {
  async fetch(req, env) {
    if (req.method !== "POST" || !new URL(req.url).pathname.endsWith("/v1/license"))
      return json({ error: "not found" }, 404);

    const { key, nonce } = await req.json().catch(() => ({}));
    if (!key || !nonce) return json({ error: "درخواست نامعتبر است" }, 400);

    // ۱) کلید باید معتبر باشد
    const rec = await env.LICENSES.get("key:" + key, "json");
    if (!rec) return json({ error: "کلید لایسنس معتبر نیست" }, 401);

    const ip = req.headers.get("CF-Connecting-IP") || "";

    // ۲) قفل IP
    let bound = rec.bound_ip;
    if (!bound) {
      bound = ip;
      await env.LICENSES.put("key:" + key, JSON.stringify({ ...rec, bound_ip: bound }));
    } else if (bound !== ip) {
      return json({ error: "این لایسنس به سرور دیگری قفل شده است" }, 403);
    }

    // ۳) ساخت و امضای توکن
    const now = Math.floor(Date.now() / 1000);
    const claims = { key, bound_ip: bound, plan: rec.plan || "pro",
      issued_at: now, expires_at: now + 48 * 3600, nonce };
    const payload = te.encode(JSON.stringify(claims));

    const seed = Uint8Array.from(atob(env.LICENSE_PRIVATE_KEY), c => c.charCodeAt(0)).slice(0, 32);
    const sig = await ed.signAsync(payload, seed);

    return json({ payload: b64(payload), sig: b64(sig), your_ip: ip }, 200);
  },
};

function json(o, s) {
  return new Response(JSON.stringify(o), { status: s, headers: { "content-type": "application/json" } });
}
```

> نکته: `JSON.stringify(claims)` در Worker باید **همان بایت‌هایی** را بدهد که در
> `payload` می‌گذاری — چون کلاینت امضا را روی بایت‌های `payload` چک می‌کند، نه روی
> JSON بازسازی‌شده. کد بالا همین کار را می‌کند (یک بار stringify، هم برای امضا هم
> برای ارسال).

## چک‌لیست استقرار

1. `wrangler secret put LICENSE_PRIVATE_KEY` با محتوای `secrets/license_ed25519_private.key`.
2. کلیدهای معتبر را در KV بگذار: `key:<KEY> → {"plan":"pro"}`.
3. Worker را روی `license.hossein-m466.workers.dev` و (در صورت تمایل) دامنه‌های دیگر
   مستقر کن؛ یا همان منطق را روی بک‌اند دلخواهت پیاده کن.
4. برای ابطال یک لایسنس، رکوردش را از KV حذف کن؛ حداکثر تا ۴۸ ساعت بعد دی‌اکتیو می‌شود.
