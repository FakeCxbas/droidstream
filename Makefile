.PHONY: all build-receiver test-receiver run-usb run-wifi run-gui setup-v4l2 setup-firewall clean build-apk install-apk

all: build-receiver

build-receiver:
	@echo "Compilando receptor CLI DroidStream..."
	cd receiver && go build -o bin/droidstream ./cmd/droidstream

test-receiver:
	@echo "Ejecutando pruebas unitarias del receptor..."
	cd receiver && go test -v ./...

run-usb: build-receiver
	@echo "Configurando tunel ADB reverse e iniciando DroidStream en modo USB con ventana interactiva..."
	@adb forward --remove-all 2>/dev/null || true
	./receiver/bin/droidstream -adb -dev /dev/video2 -gui

run-wifi: build-receiver
	@echo "Iniciando DroidStream en modo Wi-Fi con ventana interactiva..."
	./receiver/bin/droidstream -dev /dev/video2 -gui

run-gui: run-usb

setup-v4l2:
	@echo "Cargando modulo v4l2loopback para DroidStream..."
	sudo modprobe v4l2loopback devices=1 video_nr=2 card_label="DroidStream" exclusive_caps=1
	@sleep 0.5
	@v4l2-ctl --device=/dev/video2 -c keep_format=0 2>/dev/null || true
	@v4l2-ctl --device=/dev/video2 --set-fmt-video-out=width=1280,height=720,pixelformat=YU12 2>/dev/null || true

setup-firewall:
	@echo "Habilitando puertos de DroidStream en el firewall UFW..."
	sudo ufw allow 4747/tcp comment "DroidStream Video"
	sudo ufw allow 4749/udp comment "DroidStream Discovery"
	sudo ufw reload

clean:
	rm -rf receiver/bin
	cd android && ./gradlew clean

build-apk:
	@echo "Compilando APK de Android (Debug)..."
	cd android && ./gradlew assembleDebug

install-apk:
	@echo "Instalando APK en el dispositivo conectado por ADB..."
	adb install -r android/app/build/outputs/apk/debug/app-debug.apk
