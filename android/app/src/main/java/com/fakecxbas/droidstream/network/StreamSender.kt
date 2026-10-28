package com.fakecxbas.droidstream.network

import com.fakecxbas.droidstream.protocol.PacketProtocol
import kotlinx.coroutines.*
import java.io.BufferedOutputStream
import java.io.OutputStream
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.atomic.AtomicBoolean

class StreamSender(
    private val host: String,
    private val port: Int,
    private val onStateChange: (Boolean, String?) -> Unit
) {
    private val isRunning = AtomicBoolean(false)
    private var socket: Socket? = null
    private var outputStream: OutputStream? = null
    private val scope = CoroutineScope(Dispatchers.IO + SupervisorJob())

    // Buffer of 1 to keep latency minimal: if a new frame arrives before previous is sent, drop the old one
    private val frameQueue = ArrayBlockingQueue<FramePacket>(2)

    data class FramePacket(
        val frameType: Byte,
        val width: Int,
        val height: Int,
        val data: ByteArray,
        val timestampUs: Long
    )

    fun start() {
        if (isRunning.getAndSet(true)) return

        scope.launch {
            try {
                val sock = Socket()
                sock.tcpNoDelay = true
                sock.sendBufferSize = 64 * 1024
                sock.connect(InetSocketAddress(host, port), 5000)

                socket = sock
                outputStream = BufferedOutputStream(sock.getOutputStream(), 64 * 1024)

                withContext(Dispatchers.Main) {
                    onStateChange(true, null)
                }

                while (isRunning.get() && !sock.isClosed) {
                    val packet = frameQueue.poll() ?: run {
                        delay(5)
                        return@run null
                    } ?: continue

                    val header = PacketProtocol.encodeHeader(
                        frameType = packet.frameType,
                        width = packet.width,
                        height = packet.height,
                        payloadSize = packet.data.size,
                        timestampUs = packet.timestampUs
                    )

                    outputStream?.write(header)
                    outputStream?.write(packet.data)
                    outputStream?.flush()
                }
            } catch (e: Exception) {
                withContext(Dispatchers.Main) {
                    onStateChange(false, e.localizedMessage ?: "Connection error")
                }
            } finally {
                stopInternal()
            }
        }
    }

    fun sendFrame(frameType: Byte, width: Int, height: Int, data: ByteArray) {
        if (!isRunning.get()) return

        val packet = FramePacket(
            frameType = frameType,
            width = width,
            height = height,
            data = data,
            timestampUs = System.currentTimeMillis() * 1000L
        )

        // Drop oldest frame if full to prevent lag buildup
        if (!frameQueue.offer(packet)) {
            frameQueue.poll()
            frameQueue.offer(packet)
        }
    }

    fun stop() {
        isRunning.set(false)
        stopInternal()
        scope.cancel()
    }

    private fun stopInternal() {
        try {
            outputStream?.close()
        } catch (_: Exception) {}
        try {
            socket?.close()
        } catch (_: Exception) {}
        outputStream = null
        socket = null
        frameQueue.clear()
    }
}
