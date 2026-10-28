package com.fakecxbas.droidstream

import android.Manifest
import android.content.pm.PackageManager
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
            val supported = cameraManager?.setFps(nextFps) ?: false
            if (supported) {
                binding.btnFps.text = "${nextFps} FPS"
            } else {
                Toast.makeText(this, "El sensor activo no soporta ${nextFps} FPS", Toast.LENGTH_SHORT).show()
                cameraManager?.setFps(30)
                binding.btnFps.text = "30 FPS"
            }
        }

        binding.btnResolution.setOnClickListener {
            val isCurrently480p = (cameraManager?.targetWidth == 640)
            if (isCurrently480p) {
                cameraManager?.setResolution(1280, 720)
                binding.btnResolution.text = "720p"
            } else {
                cameraManager?.setResolution(640, 480)
                binding.btnResolution.text = "480p"
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

    override fun onDestroy() {
        super.onDestroy()
        disconnect()
        cameraManager?.stop()
    }
}
