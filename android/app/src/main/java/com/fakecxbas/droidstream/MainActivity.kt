package com.fakecxbas.droidstream

import android.Manifest
import android.content.pm.PackageManager
import android.os.Bundle
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
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
        cameraManager?.startCamera()
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
        }

        binding.btnTorch.setOnClickListener {
            isTorchOn = !isTorchOn
            cameraManager?.setTorch(isTorchOn)
        }
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
