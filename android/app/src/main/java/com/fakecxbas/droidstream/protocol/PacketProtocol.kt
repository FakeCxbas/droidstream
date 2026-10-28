package com.fakecxbas.droidstream.protocol

import java.nio.ByteBuffer
import java.nio.ByteOrder

object PacketProtocol {
    val MAGIC_BYTES = byteArrayOf('D'.code.toByte(), 'S'.code.toByte(), 'T'.code.toByte(), 'R'.code.toByte())
    const val VERSION: Byte = 1

    const val FRAME_TYPE_JPEG: Byte = 0x01
    const val FRAME_TYPE_YUYV: Byte = 0x02
    const val FRAME_TYPE_YU12: Byte = 0x03
    const val FRAME_TYPE_H264: Byte = 0x04

    const val HEADER_SIZE = 22

    fun encodeHeader(
        frameType: Byte,
        width: Int,
        height: Int,
        payloadSize: Int,
        timestampUs: Long
    ): ByteArray {
        val buffer = ByteBuffer.allocate(HEADER_SIZE)
        buffer.order(ByteOrder.BIG_ENDIAN)
        buffer.put(MAGIC_BYTES)
        buffer.put(VERSION)
        buffer.put(frameType)
        buffer.putShort(width.toShort())
        buffer.putShort(height.toShort())
        buffer.putInt(payloadSize)
        buffer.putLong(timestampUs)
        return buffer.array()
    }
}
