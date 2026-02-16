; Askflow Windows Installer Script
; Requires NSIS 3.0 or later

!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

; General Information
Name "Askflow"
OutFile "askflow-installer.exe"
InstallDir "$PROGRAMFILES\Askflow"
RequestExecutionLevel admin

; Variables
Var DataDir

; Interface Settings
!define MUI_ABORTWARNING
!define MUI_ICON "${NSISDIR}\Contrib\Graphics\Icons\modern-install.ico"
!define MUI_UNICON "${NSISDIR}\Contrib\Graphics\Icons\modern-uninstall.ico"

; Pages
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "..\..\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
Page custom DataDirPage DataDirPageLeave
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; Language
!insertmacro MUI_LANGUAGE "English"

; Custom Data Directory Page
Function .onInit
    ; Default data directory
    StrCpy $DataDir "$INSTDIR\data"
FunctionEnd

Function DataDirPage
    !insertmacro MUI_HEADER_TEXT "Choose Data Directory" "Select where Askflow will store its data"

    nsDialogs::Create 1018
    Pop $0

    ${NSD_CreateLabel} 0 0 100% 12u "Data Directory:"
    Pop $0

    ${NSD_CreateDirRequest} 0 13u 75% 13u $DataDir
    Pop $1

    ${NSD_CreateBrowseButton} 76% 13u 24% 13u "Browse..."
    Pop $2
    ${NSD_OnClick} $2 DataDirBrowse

    nsDialogs::Show
FunctionEnd

Function DataDirBrowse
    nsDialogs::SelectFolderDialog "Select Data Directory" $DataDir
    Pop $0
    ${If} $0 != error
        StrCpy $DataDir $0
        ${NSD_SetText} $1 $DataDir
    ${EndIf}
FunctionEnd

Function DataDirPageLeave
    ${NSD_GetText} $1 $DataDir
    ${If} $DataDir == ""
        MessageBox MB_ICONEXCLAMATION "Please specify a data directory"
        Abort
    ${EndIf}
FunctionEnd

; Installation Section
Section "Install"
    SetOutPath "$INSTDIR"

    ; Stop and remove existing service if present
    DetailPrint "Stopping existing service..."
    nsExec::ExecToLog '"$INSTDIR\askflow.exe" stop'
    Sleep 2000
    nsExec::ExecToLog '"$INSTDIR\askflow.exe" remove'
    Sleep 2000

    ; Copy executable
    DetailPrint "Installing Askflow..."
    File "..\dist\askflow.exe"

    ; Copy frontend files
    DetailPrint "Installing frontend files..."
    SetOutPath "$INSTDIR\frontend\dist"
    File /r "..\dist\frontend\dist\*.*"

    ; Create data directory
    CreateDirectory "$DataDir"
    CreateDirectory "$DataDir\logs"

    ; Write data directory path to registry
    WriteRegStr HKLM "Software\Askflow" "DataDir" "$DataDir"
    WriteRegStr HKLM "Software\Askflow" "InstallDir" "$INSTDIR"

    ; Install and start service
    SetOutPath "$INSTDIR"
    DetailPrint "Installing Windows service..."
    nsExec::ExecToLog '"$INSTDIR\askflow.exe" install --datadir="$DataDir"'
    Pop $0
    ${If} $0 != 0
        DetailPrint "Warning: Service installation returned error code $0"
    ${EndIf}

    DetailPrint "Starting service..."
    Sleep 1000
    nsExec::ExecToLog '"$INSTDIR\askflow.exe" start'
    Pop $0
    ${If} $0 != 0
        DetailPrint "Warning: Service start returned error code $0"
    ${EndIf}

    ; Create uninstaller
    WriteUninstaller "$INSTDIR\Uninstall.exe"

    ; Write uninstall registry keys
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "DisplayName" "Askflow Support Service"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "UninstallString" "$INSTDIR\Uninstall.exe"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "DisplayIcon" "$INSTDIR\askflow.exe"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "Publisher" "Vantage"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "DisplayVersion" "1.0.0"
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "NoModify" 1
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                     "NoRepair" 1

    ; Calculate and write installed size
    ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
    IntFmt $0 "0x%08X" $0
    WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow" \
                       "EstimatedSize" "$0"

    MessageBox MB_ICONINFORMATION "Installation complete!$\r$\n$\r$\nAskflow service has been installed and started.$\r$\n$\r$\nAccess the web interface at: http://localhost:8080$\r$\nData directory: $DataDir"
SectionEnd

; Uninstallation Section
Section "Uninstall"
    ; Stop and remove service
    DetailPrint "Stopping service..."
    nsExec::ExecToLog '"$INSTDIR\askflow.exe" stop'
    Sleep 2000
    DetailPrint "Removing service..."
    nsExec::ExecToLog '"$INSTDIR\askflow.exe" remove'
    Sleep 2000

    ; Read data directory from registry
    ReadRegStr $DataDir HKLM "Software\Askflow" "DataDir"

    ; Delete program files
    Delete "$INSTDIR\askflow.exe"
    Delete "$INSTDIR\Uninstall.exe"
    RMDir /r "$INSTDIR\frontend"
    RMDir "$INSTDIR"

    ; Ask about data directory
    ${If} $DataDir != ""
        MessageBox MB_YESNO|MB_ICONQUESTION \
            "Do you want to remove the data directory?$\r$\n$\r$\n$DataDir$\r$\n$\r$\nThis will delete all your data, documents, and configuration." \
            IDYES RemoveDataDir IDNO KeepDataDir

        RemoveDataDir:
            DetailPrint "Removing data directory..."
            RMDir /r "$DataDir"
            Goto EndDataDir

        KeepDataDir:
            DetailPrint "Data directory preserved at: $DataDir"

        EndDataDir:
    ${EndIf}

    ; Delete registry keys
    DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\Askflow"
    DeleteRegKey HKLM "Software\Askflow"

    MessageBox MB_ICONINFORMATION "Askflow has been uninstalled successfully."
SectionEnd
