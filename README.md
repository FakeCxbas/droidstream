# DroidStream

Sistema de transmisión de video de baja latencia para utilizar smartphones Android como cámara web virtual en sistemas Linux mediante `v4l2loopback`.

Diseñado con un enfoque minimalista y de alto rendimiento, compuesto por:
1. **Receptor CLI (Go)**: Demultiplexor concurrente que escribe directamente en el dispositivo virtual V4L2 del kernel Linux sin dependencias externas pesadas.
2. **Cliente Móvil (Android / Kotlin)**: Captura acelerada por hardware con CameraX, compresión NV21/JPEG en tiempo real y servicio en primer plano para transmisión continua con pantalla bloqueada.

---

## Arquitectura

```
+-------------------------------------------------------------+
|                     Android (Kotlin)                        |
| - CameraX API (720p / 1080p @ 30-60 fps)                    |
| - Pipeline de compresión de frames con descarte adaptativo  |
| - Foreground Service con notificación persistente           |
| - Socket TCP de baja latencia con TCP_NODELAY               |
+-------------------------------------------------------------+
                               |
         [ Cable USB vía ADB / Red Local Wi-Fi ]
                               |
                               v
+-------------------------------------------------------------+
|                    Receptor CLI (Go)                        |
| - Listener TCP concurrente                                  |
| - Parser de tramas binarias con control de desalineación    |
| - Decodificación y conversión planar YU12                   |
| - Escritura directa en /dev/videoX vía ioctl VIDIOC_S_FMT   |
+-------------------------------------------------------------+
                               |
                               v
+-------------------------------------------------------------+
|                      Kernel Linux                           |
| - v4l2loopback (/dev/video2)                                |
| - Compatible con OBS Studio, Discord, Google Meet, Zoom     |
+-------------------------------------------------------------+
```

---

## Protocolo de Tramas (DSTR)

Cada fotograma transmitido a través del socket TCP incluye un encabezado binario fijo de 22 bytes seguido del payload del frame:

| Offset | Tamaño   | Campo       | Descripción                            |
|--------|----------|-------------|----------------------------------------|
| 0      | 4 bytes  | Magic       | `0x44 0x53 0x54 0x52` ("DSTR")         |
| 4      | 1 byte   | Version     | Versión del protocolo (`0x01`)         |
| 5      | 1 byte   | FrameType   | Tipo: `0x01` JPEG, `0x02` YUYV, `0x03` YU12 |
| 6      | 2 bytes  | Width       | Ancho en píxeles (Big Endian)          |
| 8      | 2 bytes  | Height      | Alto en píxeles (Big Endian)           |
| 10     | 4 bytes  | PayloadSize | Tamaño en bytes del payload            |
| 14     | 8 bytes  | TimestampUs | Timestamp de captura en microsegundos  |
| 22     | N bytes  | Payload     | Bytes crudos del fotograma             |

---

## Requisitos del Sistema

### Linux (Host Receptor)
- Kernel Linux con soporte para `v4l2loopback`.
- Go 1.22 o superior.
- Android Debug Bridge (`adb`) para modo USB.

### Android (Dispositivo Emisor)
- Android 8.0 (API 26) o superior.
- Depuración por USB habilitada (en caso de usar conexión por cable).

---

## Guía de Uso

### 1. Preparar dispositivo virtual v4l2loopback

Si el módulo no está cargado previamente en el sistema:

```bash
sudo modprobe v4l2loopback devices=1 video_nr=2 card_label="DroidStream" exclusive_caps=1
```

Verificar que el dispositivo se encuentre disponible:

```bash
v4l2-ctl --list-devices
```

### 2. Compilar y ejecutar el receptor

Para compilar el binario:

```bash
make build-receiver
```

#### Modo USB (Recomendado para latencia cero)
Conecta el teléfono mediante cable USB con depuración habilitada y ejecuta:

```bash
make run-usb
```
Esto configurará automáticamente el túnel `adb forward tcp:4747 tcp:4747` y levantará el receptor en `/dev/video2`.

#### Modo Wi-Fi
Para operar a través de la red local sin cables:

```bash
make run-wifi
```
En el cliente Android, introduce la IP local de tu computadora y el puerto `4747`.

### 3. Compilación de la Aplicación Android

Abrir el directorio `android/` en Android Studio o compilar mediante Gradle:

```bash
cd android
./gradlew assembleDebug
```

Instalar el APK generado en el dispositivo:

```bash
adb install app/build/outputs/apk/debug/app-debug.apk
```

---

## Pruebas de Integración

Para ejecutar la suite de pruebas del receptor en Go:

```bash
make test-receiver
```

---

## Licencia

Distribuido bajo la Licencia MIT. Consulta el archivo `LICENSE` para más detalles.
