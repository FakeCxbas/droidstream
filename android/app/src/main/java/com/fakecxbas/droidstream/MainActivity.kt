package com.fakecxbas.droidstream

import android.Manifest
import android.content.pm.PackageManager
import android.content.Context
import android.net.wifi.WifiManager
import android.os.Bundle
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.camera.core.CameraSelector
import androidx.core.content.ContextCompat
import com.fakecxbas.droidstream.camera.CameraStreamManager
import com.fakecxbas.droidstream.databinding.ActivityMainBinding
import com.fakecxbas.droidstream.network.StreamSender
import com.fakecxbas.droidstream.protocol.PacketProtocol
import com.fakecxbas.droidstream.service.StreamService
import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private var cameraManager: CameraStreamManager? = null
    private var streamSender: StreamSender? = null
    private var isConnected = false
    private var isTorchOn = false

    private val permissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { isGranted ->
        if (isGranted) {
            setupCamera()
        } else {
            Toast.makeText(this, "Se requiere permiso de cámara para operar", Toast.LENGTH_LONG).show()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        setupListeners()

        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
            setupCamera()
        } else {
            permissionLauncher.launch(Manifest.permission.CAMERA)
        }
    }

    private fun setupCamera() {
        cameraManager = CameraStreamManager(
            context = this,
            lifecycleOwner = this,
            previewView = binding.previewView
        ) { frameData, width, height ->
            if (isConnected) {
                streamSender?.sendFrame(
                    frameType = PacketProtocol.FRAME_TYPE_JPEG,
                    width = width,
                    height = height,
                    data = frameData
                )
            }
        }
        cameraManager?.startCamera {
            updateCameraFacingLabel()
        }
    }

    private fun setupListeners() {
        binding.btnDetectWifi.setOnClickListener {
            discoverPcOverWifi()
        }

        binding.btnModeUsb.setOnClickListener {
            binding.etHost.setText("127.0.0.1")
            binding.etPort.setText("4747")
            Toast.makeText(this, "Modo USB seleccionado (127.0.0.1)", Toast.LENGTH_SHORT).show()
        }

        binding.btnConnect.setOnClickListener {
            if (isConnected) {
                disconnect()
            } else {
                connect()
            }
        }

        binding.btnSwitchCamera.setOnClickListener {
            cameraManager?.switchCamera()
            // Reset torch state upon camera switch since front cameras usually lack flash
            isTorchOn = false
            binding.btnTorch.text = "Flash"
            updateCameraFacingLabel()
        }

        binding.btnTorch.setOnClickListener {
            val targetState = !isTorchOn
            val success = cameraManager?.setTorch(targetState) ?: false
            if (success) {
                isTorchOn = targetState
                binding.btnTorch.text = if (isTorchOn) "Flash ON" else "Flash"
            } else {
                Toast.makeText(this, "El lente actual no dispone de flash físico", Toast.LENGTH_SHORT).show()
            }
        }

        binding.btnRotate.setOnClickListener {
            val rot = cameraManager?.rotateNext() ?: 0
            binding.btnRotate.text = "Rotar ${rot}°"
        }

        binding.btnFps.setOnClickListener {
            val currentFps = cameraManager?.targetFps ?: 30
            val nextFps = if (currentFps == 30) 60 else 30
            cameraManager?.setFps(nextFps)
            binding.btnFps.text = "${nextFps} FPS"
            if (nextFps == 60) {
                binding.btnResolution.text = "720p"
            }
        }

        binding.btnResolution.setOnClickListener {
            val isCurrently720p = (cameraManager?.targetWidth == 1280)
            if (isCurrently720p) {
                cameraManager?.setResolution(640, 480)
                binding.btnResolution.text = "480p"
                if (cameraManager?.targetFps == 60) {
                    cameraManager?.setFps(30)
                    binding.btnFps.text = "30 FPS"
                }
            } else {
                cameraManager?.setResolution(1280, 720)
                binding.btnResolution.text = "720p"
            }
        }

        binding.btnDimmer.setOnClickListener {
            binding.dimmerOverlay.visibility = android.view.View.VISIBLE
        }

        binding.dimmerOverlay.setOnClickListener {
            binding.dimmerOverlay.visibility = android.view.View.GONE
        }
    }

    private fun updateCameraFacingLabel() {
        val facing = cameraManager?.lensFacing
        val name = if (facing == CameraSelector.LENS_FACING_FRONT) "Frontal" else "Trasera"
        binding.btnSwitchCamera.text = name
    }

    private fun connect() {
        val host = binding.etHost.text?.toString()?.trim() ?: "127.0.0.1"
        val port = binding.etPort.text?.toString()?.toIntOrNull() ?: 4747

        binding.btnConnect.isEnabled = false
        binding.tvStatus.text = "Estado: Conectando..."

        streamSender = StreamSender(host, port) { connected, errorMsg ->
            isConnected = connected
            binding.btnConnect.isEnabled = true

            if (connected) {
                binding.btnConnect.text = "Desconectar"
                binding.tvStatus.text = "Estado: Transmitiendo a $host:$port"
                StreamService.startService(this)
            } else {
                binding.btnConnect.text = "Conectar"
                binding.tvStatus.text = "Estado: Desconectado" + (if (errorMsg != null) " ($errorMsg)" else "")
                StreamService.stopService(this)
            }
        }
        streamSender?.start()
    }

    private fun disconnect() {
        streamSender?.stop()
        streamSender = null
        isConnected = false
        binding.btnConnect.text = "Conectar"
        binding.tvStatus.text = "Estado: Desconectado"
        StreamService.stopService(this)
    }

    private fun discoverPcOverWifi() {
        binding.btnDetectWifi.isEnabled = false
        binding.btnDetectWifi.text = "Buscando..."

        Thread {
            var found = false
            var socket: DatagramSocket? = null
            var lock: WifiManager.MulticastLock? = null
            try {
                val wifi = applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager
                lock = wifi?.createMulticastLock("droidstream_discovery")
                lock?.setReferenceCounted(true)
                lock?.acquire()

                socket = DatagramSocket()
                socket.broadcast = true
                socket.soTimeout = 2500

                val query = "{\"query\":\"droidstream\"}".toByteArray()
                val broadcastAddr = InetAddress.getByName("255.255.255.255")
                val packet = DatagramPacket(query, query.size, broadcastAddr, 4749)
                socket.send(packet)

                val buf = ByteArray(1024)
                val responsePacket = DatagramPacket(buf, buf.size)
                socket.receive(responsePacket)

                val respStr = String(responsePacket.data, 0, responsePacket.length)
                val json = JSONObject(respStr)
                if (json.optString("service") == "droidstream") {
                    val port = json.optInt("port", 4747)
                    val ips = json.optJSONArray("ips")
                    val detectedIp = if (ips != null && ips.length() > 0) {
                        ips.getString(0)
                    } else {
                        responsePacket.address.hostAddress
                    }

                    found = true
                    runOnUiThread {
                        binding.etHost.setText(detectedIp)
                        binding.etPort.setText(port.toString())
                        Toast.makeText(this, "PC detectada: $detectedIp", Toast.LENGTH_SHORT).show()
                    }
                }
            } catch (_: Exception) {
                // Timeout or network error
            } finally {
                try {
                    socket?.close()
                } catch (_: Exception) {}
                try {
                    lock?.release()
                } catch (_: Exception) {}

                runOnUiThread {
                    binding.btnDetectWifi.isEnabled = true
                    binding.btnDetectWifi.text = "Detectar PC (Wi-Fi)"
                    if (!found) {
                        Toast.makeText(this, "No se detectó PC en la red Wi-Fi", Toast.LENGTH_SHORT).show()
                    }
                }
            }
        }.start()
    }

    override fun onDestroy() {
        super.onDestroy()
        disconnect()
        cameraManager?.stop()
    }
}
