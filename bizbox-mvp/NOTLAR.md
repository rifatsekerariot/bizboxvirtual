# BizBox MVP Projesi - Geliştirme Notları

Bu dosyada, `bizbox-mvp` projesinde bugüne kadar gerçekleştirilen tüm backend, CLI, REST API ve Web Arayüzü geliştirmeleri özetlenmiştir.

---

## 🚀 1. Go Backend & Incus İstemcisi Yetenekleri

Incus Go SDK'sı (`github.com/lxc/incus`) kullanılarak aşağıdaki temel sanal makine (VM) / konteyner yönetim fonksiyonları geliştirilmiştir:

- **CreateVM**: Belirtilen isim, imaj (örn. `images:ubuntu/22.04`), CPU limiti (`limits.cpu`) ve RAM limiti (`limits.memory`) ile `"virtual-machine"` (veya testler için konteyner) oluşturur. Uzak imaj sunucularını (`https://images.linuxcontainers.org`) otomatik olarak çözümler.
- **StartVM**: Bir makineyi başlatır (`UpdateInstanceState` API'si ile `Action: "start"`).
- **StopVM**: Bir makineyi zorla kapatmadan, temiz bir şekilde durdurur (`Action: "stop"`, `Force: false`, `Timeout: 30`).
- **DeleteVM**: Bir makineyi kalıcı olarak siler (`DeleteInstance` API'si).
- **GetVMStatus**: Belirli bir makinenin statik (`CreatedAt`, limitler) ve dinamik (`Status`, IP Adresleri) durum bilgilerini çekip birleştirir. IP adreslerinde lokal loopback (`lo`) hariç tutulup küresel IPv4 adreslerine öncelik verilir.

---

## 💻 2. CLI (Komut Satırı) Kullanımı

Uygulama derlendiğinde (`go build -o bizbox-mvp`) komut satırından doğrudan yönetilebilir:

- **Listeleme (Varsayılan)**: `./bizbox-mvp`
  - Tüm makineleri sadeleştirilmiş JSON formatında listeler.
- **VM Oluşturma**: `./bizbox-mvp create --name <isim> --cpu <çekirdek> --ram <GB> --image <imaj>`
- **VM Başlatma**: `./bizbox-mvp start --name <isim>`
- **VM Durdurma**: `./bizbox-mvp stop --name <isim>`
- **VM Durum Sorgulama**: `./bizbox-mvp status --name <isim>`
  - Detaylı durumu temiz hizalanmış bir metin tablosu olarak yazdırır.

---

## 🔌 3. REST API Sunucusu

`./bizbox-mvp serve` komutuyla başlatılan sunucu, tüm dış ağlardan erişilebilecek şekilde `0.0.0.0:8080` adresini dinler ve şu HTTP endpoint'lerini sunar:

- **GET `/api/vms`**: Tüm makineleri JSON dizisi olarak döner (HTMX harici çağrılarda).
- **GET `/api/vms/{name}`**: Belirli bir makinenin detaylı durumunu JSON formatında döner.
- **POST `/api/vms`**: Yeni makine oluşturur. (JSON gövdesi: `name`, `image`, `cpu`, `ram`).
- **POST `/api/vms/{name}/start`**: Makineyi başlatır.
- **POST `/api/vms/{name}/stop`**: Makineyi durdurur.
- **DELETE `/api/vms/{name}`**: Makineyi siler.

---

## 🎨 4. Web Arayüzü & Dashboard (HTMX)

Sistem yöneticileri için modern, kurumsal standartlarda bir web arayüzü eklenmiştir.

### Tasarım Sistemi & Arayüz Bileşenleri
- **Renkler**: `#F7F8FA` zemin, `#FFFFFF` kartlar, `#1B4B43` (aksan koyu petrol yeşili), `#16A34A` (başarı yeşili), `#DC2626` (hata kırmızısı).
- **Tipografi**: UI metinleri için **Inter** fontu, IP/RAM/CPU gibi teknik/mono veriler için **IBM Plex Mono** fontu (Google CDN).
- **Navigasyon (Sol Sabit Menü - 240px)**: Dashboard, Sanal Makineler, Ağ, Yedekleme, Ayarlar butonları (SVG ikonlu ve Türkçe etiketli). Mobil cihazlarda (`<768px`) hamburger menüye dönüşür.
- **Üst Çubuk**: Dinamik sunucu adı (hostname) ve CSS animasyonlu yeşil durum "nabız" noktası.
- **Erişilebilirlik**: `Tab` odaklanması (`outline-offset`), modal açıkken arkadaki sayfanın kaydırılmasının engellenmesi ve `ESC` tuşuyla modalların kapatılması standartlarına uyar.

### Dashboard Dinamikleri (HTMX Polling)
- **10s Yenileme**: Arayüz 10 saniyede bir `hx-get="/api/vms"` ile sayfa yenilenmeden güncellenir.
- **Loading Skeletons**: İlk yükleme esnasında veri gelene kadar parıldayan gri yükleme şablonları gösterilir.
- **Donanım Kartları (Canlı)**: Sunucunun anlık RAM kullanım yüzdesi (`/proc/meminfo` okuyarak) ve Disk kullanım yüzdesi (`syscall.Statfs` sistem çağrısı) dinamik barlarla gösterilir.
- **İşlemler Menüsü**: Tablodaki her makinenin sağındaki 3 nokta butonundan Başlat/Durdur tetiklenir, silme işleminde çift tıklamayı önleyen onay modalı açılır.
- **Boş Durum (Empty State)**: Makine yoksa kullanıcıyı yönlendiren "İlk Sisteminizi Oluşturun" ekranı belirir.
- **Hata Yönetimi**: Incus daemon bağlantısı koptuğunda arayüzde kırmızı alarm kutusu belirir ve servis geri geldiğinde otomatik kurtarılır.

### 3 Adımlı Kurulum Sihirbazı (Wizard Modal)
1. **İsim ve Sistem Tipi**: İsim girilir ve Windows Server 2022, Windows 10/11, Ubuntu, Özel ISO kartlarından biri seçilir.
2. **Kaynak Seçimi**: Küçük / Orta / Büyük hazır şablonlar veya "Gelişmiş" tıklanarak açılan manuel CPU ve RAM alanları doldurulur.
3. **Onay & Buton Kilitleme**: Seçim özeti gösterilir. "Oluştur"a basıldığında buton devre dışı kalıp "Oluşturuluyor..." yazarak çift tıklama engellenir.
- Hata durumunda hata modal içinde gösterilir; başarı durumunda tebrik mesajı gelip 1 saniye içinde modalı kapatır ve tablo HTMX event tetikleyicisiyle anında güncellenir.
