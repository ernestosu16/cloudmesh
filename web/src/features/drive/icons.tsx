import {
  File,
  FileArchive,
  FileAudio,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileVideo,
  Folder as FolderIcon,
} from 'lucide-react'

/** Icono según el tipo MIME. */
export function FileTypeIcon({ mime, className }: { mime: string; className?: string }) {
  const Icon = mime.startsWith('image/')
    ? FileImage
    : mime.startsWith('video/')
      ? FileVideo
      : mime.startsWith('audio/')
        ? FileAudio
        : mime === 'application/pdf' || mime.startsWith('text/')
          ? FileText
          : /zip|rar|7z|tar|gzip/.test(mime)
            ? FileArchive
            : /sheet|excel|csv/.test(mime)
              ? FileSpreadsheet
              : File
  return <Icon className={className} aria-hidden />
}

export { FolderIcon }
