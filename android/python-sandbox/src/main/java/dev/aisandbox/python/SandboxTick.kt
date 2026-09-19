package dev.aisandbox.python

import android.os.Parcel
import android.os.Parcelable

/** One bounded, typed SDK event. Script text and host objects are intentionally absent. */
data class SandboxTick(
    val sessionId: String,
    val sequence: Long,
    val tick: Long,
    val eventType: Int,
    val targetId: String?,
    val value: Float,
) : Parcelable {
    constructor(parcel: Parcel) : this(
        sessionId = requireNotNull(parcel.readString()),
        sequence = parcel.readLong(),
        tick = parcel.readLong(),
        eventType = parcel.readInt(),
        targetId = parcel.readString(),
        value = parcel.readFloat(),
    )

    override fun writeToParcel(parcel: Parcel, flags: Int) {
        parcel.writeString(sessionId)
        parcel.writeLong(sequence)
        parcel.writeLong(tick)
        parcel.writeInt(eventType)
        parcel.writeString(targetId)
        parcel.writeFloat(value)
    }

    override fun describeContents() = 0

    companion object CREATOR : Parcelable.Creator<SandboxTick> {
        override fun createFromParcel(parcel: Parcel) = SandboxTick(parcel)
        override fun newArray(size: Int): Array<SandboxTick?> = arrayOfNulls(size)
    }
}
