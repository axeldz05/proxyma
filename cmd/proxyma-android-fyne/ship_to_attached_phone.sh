#!/usr/bin/env bash

BOLD='\033[1;32m'
ERROR='\033[1;31m'
INFO='\033[1;34m'
NC='\033[0m'

echo -e "${INFO}[1/4] Asegurando variables de entorno para Android...${NC}"
export ANDROID_HOME=/opt/android-sdk
export ANDROID_NDK_HOME=/opt/android-ndk
export JAVA_HOME=/usr/lib/jvm/default
export PATH=$PATH:$ANDROID_HOME/build-tools/35.0.0:$ANDROID_HOME/platform-tools:$JAVA_HOME/bin:/home/drusila/go/bin

echo -e "${INFO}[2/5] Buscando dispositivo físico conectado por USB...${NC}"
DEVICE_SERIAL=$(adb devices -l | grep -v -E "emulator-|List of" | grep "device" | awk '{print $1}' | head -n 1)

if [ -z "$DEVICE_SERIAL" ]; then
    echo -e "${ERROR}❌ ERROR: No se detecta ningún celular conectado por USB.${NC}"
    exit 1
fi
echo -e "${BOLD}✔ Dispositivo detectado correctamente (Serial: $DEVICE_SERIAL).${NC}"

echo -e "${INFO}[3/5] Compilando APK de Debug con Fyne...${NC}"
fyne package -os android -app-id com.proxyma.android -icon icon.png

if [ $? -ne 0 ]; then
    echo -e "${ERROR}❌ ERROR: Falló la compilación del paquete Fyne.${NC}"
    exit 1
fi

echo -e "${INFO}[4/5] Vaciando buffer circular de Android Logcat...${NC}"
adb -s "$DEVICE_SERIAL" logcat -c

echo -e "${INFO}[5/5] Inyectando Proxyma_Node.apk en el celular (Serial: $DEVICE_SERIAL)...${NC}"

if [ ! -f "$APK_PATH" ] && [ -f "./Proxyma_Node.apk" ]; then
    APK_PATH="./Proxyma_Node.apk"
fi

adb -s "$DEVICE_SERIAL" install -r "$APK_PATH"

if [ $? -eq 0 ]; then
    echo -e "${BOLD}🚀 ¡Proxyma desplegado con éxito en tu celular!${NC}"
    echo -e "${INFO}Podés iniciar tu debug en tiempo real ejecutando:${NC}"
    echo -e "${BOLD}pidcat -c com.proxyma.android${NC}"
else
    echo -e "${ERROR}❌ Falló la instalación del APK en el dispositivo.${NC}"
    exit 1
fi
