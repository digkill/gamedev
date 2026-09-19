package dev.aisandbox.python

import android.os.Parcel
import android.os.Parcelable

data class SandboxReply(
    val status: Int,
    val acceptedSequence: Long,
) : Parcelable {
    constructor(parcel: Parcel) : this(parcel.readInt(), parcel.readLong())

    override fun writeToParcel(parcel: Parcel, flags: Int) {
        parcel.writeInt(status)
        parcel.writeLong(acceptedSequence)
    }

    override fun describeContents() = 0

    companion object CREATOR : Parcelable.Creator<SandboxReply> {
        const val STATUS_INTERPRETER_UNAVAILABLE = 1
        const val STATUS_INVALID_REQUEST = 2

        override fun createFromParcel(parcel: Parcel) = SandboxReply(parcel)
        override fun newArray(size: Int): Array<SandboxReply?> = arrayOfNulls(size)
    }
}
