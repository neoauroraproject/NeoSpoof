# NeoSpoof

پنل تست اسپوف. با یک دستور نصب می‌شود، یا با کپی خود فایل و یک دستور بالا می‌آید.

ورود دمو: نام کاربری `demo` و رمز `demo`.

## نصب با یک خط

```bash
curl -fsSL https://raw.githubusercontent.com/neoauroraproject/NeoSpoof/main/install.sh | sudo sh
```

این دستور فایل را در `/usr/local/bin/neospoof` می‌گذارد و همان لحظه پنل را بالا می‌آورد. آدرس و پورت تصادفی در خروجی چاپ می‌شود.

## بالا آوردن با کپی فایل

فایل `neospoof-linux-amd64` را روی سرور بگذارید، سپس:

```bash
chmod +x neospoof-linux-amd64 && sudo ./neospoof-linux-amd64
```

تست بسته به لینوکس و `sudo` نیاز دارد. خود پنل روی ویندوز هم باز می‌شود.
